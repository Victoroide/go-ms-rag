package extractor

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

type docxExtractor struct{}

func (e *docxExtractor) Type() FileType { return FileTypeDOCX }

func (e *docxExtractor) Extract(ctx context.Context, r io.Reader) (string, error) {
	data, err := readSource(ctx, r)
	if err != nil {
		return "", fmt.Errorf("extractor: docx: read: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("extractor: docx: open zip: %w", err)
	}

	var docFile *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docFile = f
			break
		}
	}
	if docFile == nil {
		return "", fmt.Errorf("extractor: docx: word/document.xml not found")
	}

	if docFile.UncompressedSize64 > maxDocumentXMLBytes {
		return "", fmt.Errorf("extractor: docx: document XML exceeds maximum size of %d bytes", maxDocumentXMLBytes)
	}
	rc, err := docFile.Open()
	if err != nil {
		return "", fmt.Errorf("extractor: docx: open document.xml: %w", err)
	}
	defer func() { _ = rc.Close() }()

	text, err := parseDocumentXML(contextReader{ctx: ctx, reader: io.LimitReader(rc, maxDocumentXMLBytes+1)})
	if err != nil {
		return "", fmt.Errorf("extractor: docx: %w", err)
	}

	text = strings.TrimSpace(text)
	if err := checkTextLimit(len(text)); err != nil {
		return "", err
	}
	if len(text) < 10 {
		return "", fmt.Errorf("extractor: docx: no readable text found")
	}
	return text, nil
}

// parseDocumentXML walks <w:p> and <w:t> elements to collect text.
func parseDocumentXML(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)
	var paragraphs []string
	var paragraph strings.Builder
	textBytes := 0
	inParagraph, inText := false, false
	const wordNamespace = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parse xml: %w", err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Space != wordNamespace {
				continue
			}
			switch t.Name.Local {
			case "p":
				inParagraph = true
				paragraph.Reset()
			case "t":
				inText = inParagraph
			case "tab", "br", "cr":
				if inParagraph {
					textBytes++
					if err := checkTextLimit(textBytes); err != nil {
						return "", err
					}
					separator := byte('\n')
					if t.Name.Local == "tab" {
						separator = '\t'
					}
					paragraph.WriteByte(separator)
				}
			}
		case xml.EndElement:
			if t.Name.Space != wordNamespace {
				continue
			}
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if text := strings.TrimSpace(paragraph.String()); text != "" {
					if len(paragraphs) > 0 {
						textBytes++
						if err := checkTextLimit(textBytes); err != nil {
							return "", err
						}
					}
					paragraphs = append(paragraphs, text)
				}
				inParagraph, inText = false, false
			}
		case xml.CharData:
			if inText {
				textBytes += len(t)
				if err := checkTextLimit(textBytes); err != nil {
					return "", err
				}
				paragraph.Write(t)
			}
		}
	}
	return strings.Join(paragraphs, "\n"), nil
}

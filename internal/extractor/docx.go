package extractor

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

type docxExtractor struct{}

func (e *docxExtractor) Type() FileType { return FileTypeDOCX }

func (e *docxExtractor) Extract(_ context.Context, r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
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

	rc, err := docFile.Open()
	if err != nil {
		return "", fmt.Errorf("extractor: docx: open document.xml: %w", err)
	}
	defer rc.Close()

	text, err := parseDocumentXML(rc)
	if err != nil {
		return "", fmt.Errorf("extractor: docx: %w", err)
	}

	text = strings.TrimSpace(text)
	if len(text) < 10 {
		return "", fmt.Errorf("extractor: docx: no readable text found")
	}
	return text, nil
}

// parseDocumentXML walks <w:p> and <w:t> elements to collect text.
func parseDocumentXML(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)
	var paragraphs []string
	var currentRuns []string
	inParagraph := false

	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parse xml: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "p" && t.Name.Space == "http://schemas.openxmlformats.org/wordprocessingml/2006/main" {
				inParagraph = true
				currentRuns = currentRuns[:0]
			}
		case xml.EndElement:
			if t.Name.Local == "p" && t.Name.Space == "http://schemas.openxmlformats.org/wordprocessingml/2006/main" {
				if inParagraph && len(currentRuns) > 0 {
					paragraphs = append(paragraphs, strings.Join(currentRuns, " "))
				}
				inParagraph = false
			}
		case xml.CharData:
			if inParagraph {
				text := strings.TrimSpace(string(t))
				if text != "" {
					currentRuns = append(currentRuns, text)
				}
			}
		}
	}

	return strings.Join(paragraphs, "\n"), nil
}

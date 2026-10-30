package extractor

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

const (
	docxWordNamespace          = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	docxStrictNamespace        = "http://purl.oclc.org/ooxml/wordprocessingml/main"
	docxCompatibilityNamespace = "http://schemas.openxmlformats.org/markup-compatibility/2006"
	maxDOCXRelationshipsBytes  = 1 << 20
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

	mainPart, err := docxMainPart(ctx, zr)
	if err != nil {
		return "", fmt.Errorf("extractor: docx: %w", err)
	}

	var collected docxText
	for _, name := range []string{mainPart, "word/footnotes.xml", "word/endnotes.xml"} {
		part := docxPart(zr, name)
		if part == nil {
			if name == mainPart {
				return "", fmt.Errorf("extractor: docx: %s not found", name)
			}
			continue
		}
		if err := collected.parsePart(ctx, part); err != nil {
			return "", fmt.Errorf("extractor: docx: %s: %w", name, err)
		}
	}

	text := strings.TrimSpace(strings.Join(collected.paragraphs, "\n"))
	if err := checkTextLimit(len(text)); err != nil {
		return "", err
	}
	if len(text) < 10 {
		return "", fmt.Errorf("extractor: docx: no readable text found")
	}
	return text, nil
}

func docxPart(zr *zip.Reader, name string) *zip.File {
	for _, part := range zr.File {
		if part.Name == name {
			return part
		}
	}
	return nil
}

func docxMainPart(ctx context.Context, zr *zip.Reader) (string, error) {
	part := docxPart(zr, "_rels/.rels")
	if part == nil {
		return "word/document.xml", nil
	}
	if part.UncompressedSize64 > maxDOCXRelationshipsBytes {
		return "", fmt.Errorf("relationships XML exceeds maximum size of %d bytes", maxDOCXRelationshipsBytes)
	}
	rc, err := part.Open()
	if err != nil {
		return "", fmt.Errorf("open relationships: %w", err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(contextReader{ctx: ctx, reader: io.LimitReader(rc, maxDOCXRelationshipsBytes+1)})
	if err != nil {
		return "", fmt.Errorf("read relationships: %w", err)
	}
	if len(data) > maxDOCXRelationshipsBytes {
		return "", fmt.Errorf("relationships XML exceeds maximum size of %d bytes", maxDOCXRelationshipsBytes)
	}
	var relationships struct {
		Entries []struct {
			Type       string `xml:"Type,attr"`
			Target     string `xml:"Target,attr"`
			TargetMode string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(data, &relationships); err != nil {
		return "", fmt.Errorf("parse relationships: %w", err)
	}
	for _, relationship := range relationships.Entries {
		if strings.HasSuffix(relationship.Type, "/officeDocument") && relationship.Target != "" && relationship.TargetMode != "External" {
			return path.Clean(strings.TrimPrefix(relationship.Target, "/")), nil
		}
	}
	return "word/document.xml", nil
}

type docxText struct {
	paragraphs []string
	textBytes  int
}

func (text *docxText) parsePart(ctx context.Context, part *zip.File) error {
	if part.UncompressedSize64 > maxDocumentXMLBytes {
		return fmt.Errorf("document XML exceeds maximum size of %d bytes", maxDocumentXMLBytes)
	}
	rc, err := part.Open()
	if err != nil {
		return fmt.Errorf("open XML: %w", err)
	}
	defer func() { _ = rc.Close() }()
	return text.parseXML(contextReader{ctx: ctx, reader: io.LimitReader(rc, maxDocumentXMLBytes+1)})
}

func isDOCXWordNamespace(namespace string) bool {
	return namespace == docxWordNamespace || namespace == docxStrictNamespace
}

func (text *docxText) addBytes(n int) error {
	text.textBytes += n
	return checkTextLimit(text.textBytes)
}

func (text *docxText) parseXML(r io.Reader) error {
	decoder := xml.NewDecoder(r)
	var paragraphs []*strings.Builder
	inText := false
	skipDepth := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("parse xml: %w", err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			if skipDepth > 0 {
				skipDepth++
				continue
			}
			if t.Name.Space == docxCompatibilityNamespace && t.Name.Local == "Fallback" {
				skipDepth = 1
				continue
			}
			if !isDOCXWordNamespace(t.Name.Space) {
				continue
			}
			switch t.Name.Local {
			case "footnote", "endnote":
				for _, attr := range t.Attr {
					if isDOCXWordNamespace(attr.Name.Space) && attr.Name.Local == "type" && (attr.Value == "separator" || attr.Value == "continuationSeparator") {
						skipDepth = 1
						break
					}
				}
			case "p":
				paragraphs = append(paragraphs, &strings.Builder{})
				inText = false
			case "t":
				inText = len(paragraphs) > 0
			case "tab", "br", "cr", "noBreakHyphen":
				if len(paragraphs) > 0 {
					if err := text.addBytes(1); err != nil {
						return err
					}
					separator := byte('\n')
					switch t.Name.Local {
					case "tab":
						separator = '\t'
					case "noBreakHyphen":
						separator = '-'
					}
					_ = paragraphs[len(paragraphs)-1].WriteByte(separator)
				}
			}
		case xml.EndElement:
			if skipDepth > 0 {
				skipDepth--
				continue
			}
			if !isDOCXWordNamespace(t.Name.Space) {
				continue
			}
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				paragraph := paragraphs[len(paragraphs)-1]
				paragraphs = paragraphs[:len(paragraphs)-1]
				if value := strings.TrimSpace(paragraph.String()); value != "" {
					if len(text.paragraphs) > 0 {
						if err := text.addBytes(1); err != nil {
							return err
						}
					}
					text.paragraphs = append(text.paragraphs, value)
				}
				inText = false
			}
		case xml.CharData:
			if skipDepth == 0 && inText {
				if err := text.addBytes(len(t)); err != nil {
					return err
				}
				_, _ = paragraphs[len(paragraphs)-1].Write(t)
			}
		}
	}
	return nil
}

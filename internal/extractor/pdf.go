package extractor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/ledongthuc/pdf"
)

type pdfExtractor struct{}

func (e *pdfExtractor) Type() FileType { return FileTypePDF }

var multiBlankLine = regexp.MustCompile(`\n{3,}`)

func (e *pdfExtractor) Extract(_ context.Context, r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("extractor: pdf: read: %w", err)
	}

	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("extractor: pdf: %w", err)
	}

	var buf strings.Builder
	numPages := reader.NumPage()
	for i := 1; i <= numPages; i++ {
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, pErr := page.GetPlainText(nil)
		if pErr != nil {
			continue
		}
		cleaned := stripControlChars(text)
		cleaned = strings.TrimSpace(cleaned)
		if cleaned == "" {
			continue
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(cleaned)
	}

	result := multiBlankLine.ReplaceAllString(buf.String(), "\n\n")
	result = strings.TrimSpace(result)
	if len(result) < 10 {
		return "", fmt.Errorf("extractor: pdf: no readable text found")
	}
	return result, nil
}

func stripControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r != 0x7F) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

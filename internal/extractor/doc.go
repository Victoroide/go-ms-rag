package extractor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/richardlehane/mscfb"
)

type docExtractor struct{}

func (e *docExtractor) Type() FileType { return FileTypeDOC }

func (e *docExtractor) Extract(ctx context.Context, r io.Reader) (string, error) {
	data, err := readSource(ctx, r)
	if err != nil {
		return "", fmt.Errorf("extractor: doc: read: %w", err)
	}

	doc, err := mscfb.New(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("extractor: doc: %w — convert to .docx and retry", err)
	}

	var wordData []byte
	for entry, err := doc.Next(); err == nil; entry, err = doc.Next() {
		if entry.Name == "WordDocument" {
			wordData, err = io.ReadAll(entry)
			if err != nil {
				return "", fmt.Errorf("extractor: doc: read WordDocument stream: %w", err)
			}
			break
		}
	}

	if len(wordData) == 0 {
		return "", fmt.Errorf("extractor: doc: WordDocument stream not found — convert to .docx and retry")
	}

	text := extractPrintableText(wordData)
	text = strings.TrimSpace(text)
	if len(text) < 10 {
		return "", fmt.Errorf("extractor: doc: no readable text found")
	}
	return text, nil
}

// extractPrintableText scans raw bytes for printable ASCII runs.
// Runs shorter than 3 characters are filtered as metadata noise.
func extractPrintableText(data []byte) string {
	var runs []string
	var current bytes.Buffer

	for _, b := range data {
		if (b >= 0x20 && b <= 0x7E) || b == '\t' || b == '\n' || b == '\r' {
			current.WriteByte(b)
		} else {
			if current.Len() >= 3 {
				runs = append(runs, strings.TrimSpace(current.String()))
			}
			current.Reset()
		}
	}
	if current.Len() >= 3 {
		runs = append(runs, strings.TrimSpace(current.String()))
	}

	// Filter empty runs after trimming.
	filtered := make([]string, 0, len(runs))
	for _, r := range runs {
		if r != "" {
			filtered = append(filtered, r)
		}
	}

	return strings.Join(filtered, " ")
}

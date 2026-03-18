package extractor

import (
	"context"
	"fmt"
	"io"
	"strings"
)

type textExtractor struct{}

func (e *textExtractor) Type() FileType { return FileTypeText }

func (e *textExtractor) Extract(_ context.Context, r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("extractor: text: %w", err)
	}
	text := strings.TrimSpace(string(data))
	if len(text) < 10 {
		return "", fmt.Errorf("extractor: text: no readable text found")
	}
	return text, nil
}

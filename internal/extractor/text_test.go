package extractor

import (
	"errors"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
)

func TestTextExtractorRejectsTextAboveContentLimit(t *testing.T) {
	for name, text := range map[string]string{
		"ascii":   strings.Repeat("x", domain.MaxContentBytes+1),
		"unicode": strings.Repeat("界", domain.MaxContentBytes/3+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(FileTypeText).Extract(t.Context(), strings.NewReader(text))
			if !errors.Is(err, ErrContentTooLarge) {
				t.Fatalf("expected the text limit, got %v", err)
			}
		})
	}
}

func TestTextExtractorAcceptsTextAtContentLimit(t *testing.T) {
	text, err := New(FileTypeText).Extract(t.Context(), strings.NewReader(" \n"+strings.Repeat("x", domain.MaxContentBytes)+"\n "))
	if err != nil {
		t.Fatal(err)
	}
	if len(text) != domain.MaxContentBytes {
		t.Fatalf("returned %d text bytes", len(text))
	}
}

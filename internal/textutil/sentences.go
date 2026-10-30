package textutil

import (
	"regexp"
	"strings"
)

// SplitSentences splits after terminal punctuation, keeping punctuation attached.
// ASCII terminators require following whitespace; full-width terminators do not.
func SplitSentences(paragraph string) []string {
	if paragraph == "" {
		return nil
	}
	matches := sentenceBoundaryRe.FindAllStringIndex(paragraph, -1)
	if len(matches) == 0 {
		return []string{paragraph}
	}
	out := make([]string, 0, len(matches)+1)
	prev := 0
	for _, m := range matches {
		piece := strings.TrimSpace(paragraph[prev:m[1]])
		if piece != "" {
			out = append(out, piece)
		}
		prev = m[1]
	}
	if tail := strings.TrimSpace(paragraph[prev:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

var sentenceBoundaryRe = regexp.MustCompile(`[.!?…]+\s+|[。！？；｡]+\s*`)

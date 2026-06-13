package chunker

import (
	"regexp"
	"strings"
	"unicode"
)

// ChunkKind describes what the chunk *looks like* semantically.
// This is a heuristic — never a hard authority — so retrieval and the
// frontend can de-prioritise low-value evidence without losing it.
type ChunkKind string

const (
	// KindBody is the default: real prose, plausibly meaningful content.
	KindBody ChunkKind = "body"
	// KindContact means the chunk is dominated by email addresses,
	// phone numbers and URLs (the typical header of a CV).
	KindContact ChunkKind = "contact"
	// KindHeader means the chunk is mostly section-title fragments
	// in all-caps without prose ("EXPERIENCIA EDUCACIÓN HABILIDADES").
	KindHeader ChunkKind = "header"
)

// ChunkQuality is a coarse signal: "high" means the chunk is likely to
// produce useful evidence; "low" means the chunk is mostly noise and
// should be down-ranked when surfaced as evidence.
type ChunkQuality string

const (
	QualityHigh ChunkQuality = "high"
	QualityLow  ChunkQuality = "low"
)

var (
	emailLikeRe = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`)
	urlLikeRe   = regexp.MustCompile(`\b(?:https?://|www\.)\S+`)
	phoneLikeRe = regexp.MustCompile(`\+?\d[\d\s().-]{7,}\d`)
)

// Classify returns a (kind, quality) pair for the given chunk text.
// Rules are intentionally conservative — we never tag a chunk as
// "low quality" unless it's overwhelmingly contact info or section
// labels. Genuine evidence with a stray email at the top stays
// classified as body/high.
func Classify(text string) (ChunkKind, ChunkQuality) {
	t := strings.TrimSpace(text)
	if t == "" {
		return KindBody, QualityLow
	}

	// Strip contact substrings; what remains is "prose-like" content.
	stripped := emailLikeRe.ReplaceAllString(t, " ")
	stripped = urlLikeRe.ReplaceAllString(stripped, " ")
	stripped = phoneLikeRe.ReplaceAllString(stripped, " ")
	stripped = strings.TrimSpace(stripped)

	totalLetters := countLetters(t)
	strippedLetters := countLetters(stripped)
	// If removing contact info leaves us with less than ~30% of the
	// original letter count, the chunk was dominated by contact.
	if totalLetters > 0 && strippedLetters < totalLetters*30/100 {
		return KindContact, QualityLow
	}

	// All-caps short fragments (section headers) — fewer than 12 words
	// total and ratio of uppercase letters above 70% of letters.
	words := strings.Fields(t)
	if len(words) > 0 && len(words) <= 12 {
		upper := countUppercase(t)
		if totalLetters > 0 && upper*100/totalLetters > 70 {
			return KindHeader, QualityLow
		}
	}

	// Very short (< 6 words) AND no useful prose markers — treat as low.
	if len(words) < 6 {
		return KindBody, QualityLow
	}

	return KindBody, QualityHigh
}

func countLetters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

func countUppercase(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsUpper(r) {
			n++
		}
	}
	return n
}

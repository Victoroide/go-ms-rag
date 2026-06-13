// Package textutil provides text normalization helpers used by the
// ingestion pipeline. The goal is to clean noise that hurts retrieval
// quality (PDF extraction artefacts, decorative junk, soft line breaks)
// while preserving semantic context — paragraph structure, real words,
// section meaning. Nothing in here is destructive: we never delete real
// prose, only re-flow it.
package textutil

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// NormalizeForIngest cleans a freshly extracted document so that the
// chunker downstream sees well-formed prose. The order of operations
// matters and is deliberately conservative:
//
//  1. NFKC unicode normalization (folds ligatures, full-width digits,
//     compatibility CJK forms — common PDF artefacts).
//  2. Strip true control characters and zero-width junk, but keep
//     newlines and tabs since they encode structure.
//  3. Fix soft hyphenation across line breaks ("develop-\n  oper" →
//     "developer"). PDF extractors emit these because the original
//     document was justified.
//  4. Collapse runs of decorative ASCII (---, ===, ...) and bullet
//     character runs (•••, ▪▪) into a single space so they don't dilute
//     the embedding.
//  5. Convert single soft line breaks inside a paragraph to a space.
//     Two or more consecutive newlines are paragraph separators and are
//     preserved as a single "\n\n" so the chunker can see structure.
//  6. Trim trailing whitespace on each line.
//
// The returned string is suitable for both human reading (cleaner) and
// for embedding (less noise per chunk).
func NormalizeForIngest(raw string) string {
	if raw == "" {
		return ""
	}
	// 1. NFKC normalization.
	s := norm.NFKC.String(raw)
	// 2. Strip control characters but keep \n and \t.
	s = stripControlExceptStructure(s)
	// 3. Fix hyphenation across line breaks.
	s = hyphenLineBreakRe.ReplaceAllString(s, "")
	// 4. Collapse decorative runs.
	s = decorationRunRe.ReplaceAllString(s, " ")
	s = bulletRunRe.ReplaceAllString(s, " ")
	// 5. Re-flow paragraphs:
	//    - normalize CRLF / CR to LF
	//    - collapse 2+ newlines to a single placeholder so we don't
	//      eat real paragraph boundaries when we join soft lines
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = paragraphBreakRe.ReplaceAllString(s, paragraphPlaceholder)
	// Now any remaining single \n is a soft wrap inside a paragraph.
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, paragraphPlaceholder, "\n\n")
	// 6. Final tidy: collapse runs of horizontal whitespace, trim each
	//    line, trim outer.
	s = horizontalSpaceRe.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

const paragraphPlaceholder = "\x00P\x00"

var (
	// hyphenLineBreakRe matches a hyphen at end-of-line followed by
	// whitespace — the typical PDF-justified word break.
	hyphenLineBreakRe = regexp.MustCompile(`-\s*\n\s*`)
	// decorationRunRe matches 3+ of the same decoration char run
	// (---, ===, ___, ***, ~~~, ...) which never carry meaning.
	decorationRunRe = regexp.MustCompile(`(?:[\-=_*~]{3,})|(?:\.{4,})`)
	// bulletRunRe matches runs of bullet-like characters.
	bulletRunRe = regexp.MustCompile(`[•●◦▪▫■□★☆·]{2,}`)
	// paragraphBreakRe matches 2+ newlines, possibly mixed with
	// horizontal whitespace.
	paragraphBreakRe = regexp.MustCompile(`\n[\t ]*(?:\n[\t ]*)+`)
	// horizontalSpaceRe matches runs of horizontal whitespace (NOT
	// newlines) so we don't accidentally collapse paragraph breaks.
	horizontalSpaceRe = regexp.MustCompile(`[\t ]+`)
)

func stripControlExceptStructure(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case isZeroWidth(r):
			// Zero-width chars (ZWSP, ZWNJ, ZWJ, BOM, etc.) — drop entirely.
			continue
		case unicode.IsControl(r):
			// Replace control chars with a space so word boundaries
			// survive (e.g. "Python\x0cDjango" becomes "Python Django").
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isZeroWidth recognises the zero-width characters that frequently
// leak in from PDF/DOCX extraction (most often the BOM and the various
// zero-width spaces). Encoded by rune value so the Go source file
// itself stays free of stray BOMs.
func isZeroWidth(r rune) bool {
	switch r {
	case 0x200B, // ZERO WIDTH SPACE
		0x200C,                                 // ZERO WIDTH NON-JOINER
		0x200D,                                 // ZERO WIDTH JOINER
		0x200E,                                 // LEFT-TO-RIGHT MARK
		0x200F,                                 // RIGHT-TO-LEFT MARK
		0x202A, 0x202B, 0x202C, 0x202D, 0x202E, // bidi formatting
		0x2060, // WORD JOINER
		0xFEFF: // ZERO WIDTH NO-BREAK SPACE / BOM
		return true
	}
	return false
}

// SplitParagraphs returns the paragraphs of a normalized document.
// Empty paragraphs are dropped. Callers should pass a string that has
// already been through NormalizeForIngest.
func SplitParagraphs(text string) []string {
	if text == "" {
		return nil
	}
	parts := strings.Split(text, "\n\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// SplitSentences performs a conservative sentence split on a single
// paragraph. We split on terminal punctuation followed by whitespace.
// Perfect linguistic accuracy is not required because the chunker only
// uses these as preferred break points — even if "Dr. Pérez" gets cut,
// no content is lost; the words end up in adjacent sentences.
func SplitSentences(paragraph string) []string {
	if paragraph == "" {
		return nil
	}
	matches := sentenceBoundaryRe.FindAllStringSubmatchIndex(paragraph, -1)
	if len(matches) == 0 {
		return []string{paragraph}
	}
	out := make([]string, 0, len(matches)+1)
	prev := 0
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		// m[2] is the start of the whitespace capture, i.e. just
		// past the terminal punctuation. Use it as the end of the
		// current sentence so the punctuation stays attached.
		end := m[2]
		piece := strings.TrimSpace(paragraph[prev:end])
		if piece != "" {
			out = append(out, piece)
		}
		// m[3] is the end of the whitespace — i.e. the start of the
		// next sentence.
		prev = m[3]
	}
	tail := strings.TrimSpace(paragraph[prev:])
	if tail != "" {
		out = append(out, tail)
	}
	if len(out) == 0 {
		return []string{paragraph}
	}
	return out
}

// sentenceBoundaryRe matches a run of terminal punctuation followed by
// one-or-more whitespace characters. The whitespace is in a capture
// group so the caller can locate the boundary precisely.
var sentenceBoundaryRe = regexp.MustCompile(`[.!?…]+(\s+)`)

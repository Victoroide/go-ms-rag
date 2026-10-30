package chunker

import (
	"html"
	"regexp"
)

var (
	htmlClosingTagRe = regexp.MustCompile(`(?i)</[a-z][a-z0-9]*\s*>`)
	htmlOpeningTagRe = regexp.MustCompile(`(?i)<(?:p|div|span|br|li|ul|ol|table|tr|td|th|h[1-6]|a|b|i|em|strong|html|body|head|title|script|style)\b[^<>]*>`)
	htmlCommentRe    = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlScriptRe     = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	htmlStyleRe      = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)
	htmlBlockTagRe   = regexp.MustCompile(`(?i)</?(?:p|div|li|tr|h[1-6]|table|ul|ol|section|article|header|footer)(?:\s[^<>]*)?/?>`)
	htmlBreakTagRe   = regexp.MustCompile(`(?i)</?br(?:\s[^<>]*)?/?>`)
	htmlElementRe    = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9:-]*(?:\s[^<>]*)?/?>`)
)

func looksLikeHTML(s string) bool {
	return htmlClosingTagRe.MatchString(s) || htmlOpeningTagRe.MatchString(s)
}

func stripHTML(s string) string {
	if !looksLikeHTML(s) {
		return s
	}
	s = htmlCommentRe.ReplaceAllString(s, "")
	s = htmlScriptRe.ReplaceAllString(s, "")
	s = htmlStyleRe.ReplaceAllString(s, "")
	s = htmlBlockTagRe.ReplaceAllString(s, "\n\n")
	s = htmlBreakTagRe.ReplaceAllString(s, "\n")
	s = htmlElementRe.ReplaceAllString(s, "")
	return html.UnescapeString(s)
}

package pipeline

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// stopWords are common English words that should not be treated as named entities
// even when capitalised (e.g. at the start of a sentence or title).
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true,
	"for": true, "in": true, "on": true, "at": true, "to": true,
	"of": true, "with": true, "by": true, "from": true, "is": true,
	"are": true, "was": true, "were": true, "be": true, "been": true,
	"document": true, "reference": true, "guide": true, "report": true,
	"title": true, "section": true, "summary": true, "overview": true,
	"what": true, "how": true, "which": true, "who": true, "find": true, "list": true,

	"describe": true, "tell": true, "compare": true, "best": true,
}

// isStopWord returns true if the lowercased word is a generic stop word.
func isStopWord(word string) bool {
	// Strip trailing punctuation before checking.
	clean := strings.TrimRight(strings.ToLower(word), ":;,.-!?")
	return stopWords[clean]
}

// ExtractNamedEntity finds a proper noun phrase with at least two capitalised,
// non-stop words so generic document titles are ignored.
func ExtractNamedEntity(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	words := strings.Fields(text)

	i := 0
	for i < len(words) {
		word := words[i]

		// Skip stop words and non-capitalised words.
		if isStopWord(word) || !isCapitalised(word) {
			i++
			continue
		}

		// Start collecting an entity phrase.
		var entityWords []string
		entityWords = append(entityWords, word)
		properWords := 1
		i++

		for i < len(words) {
			next := words[i]
			lower := strings.ToLower(next)
			// Allow name connectors (de, del, la, von).
			isConnector := lower == "de" || lower == "del" || lower == "la" || lower == "von"
			if isConnector || (isCapitalised(next) && !isStopWord(next)) {
				entityWords = append(entityWords, next)
				if !isConnector {
					properWords++
				}
				i++
			} else {
				break
			}
		}

		// Only accept phrases of 2+ non-connector words as proper entities.
		if properWords >= 2 {
			return strings.ToLower(strings.Join(entityWords, " "))
		}

		// Single capitalised word — skip, too ambiguous.
	}

	return ""
}

// isCapitalised returns true if the first rune of word is an uppercase letter.
func isCapitalised(word string) bool {
	if word == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(word)
	return unicode.IsLetter(r) && unicode.IsUpper(r)
}

// MatchNamedEntity selects the strongest whole-word match in candidate order.
func MatchNamedEntity(query string, previews map[string]string, order []string) (entity, docID string) {
	queryTokens := entityTokens(query)
	bestClass, bestLength := 0, 0
	for _, id := range order {
		candidate := ExtractNamedEntity(previews[id])
		tokens := entityTokens(candidate)
		if len(tokens) == 0 {
			continue
		}
		matchClass, matchLength := 0, 0
		for start := 0; start+len(tokens) <= len(queryTokens); start++ {
			matches := true
			for i, token := range tokens {
				if queryTokens[start+i] != token {
					matches = false
					break
				}
			}
			if matches {
				matchClass = 2
				matchLength = utf8.RuneCountInString(candidate)
				break
			}
		}
		if matchClass == 0 {
			for _, token := range tokens {
				if utf8.RuneCountInString(token) <= 2 {
					continue
				}
				for _, word := range queryTokens {
					if word == token {
						matchClass = 1
						matchLength = max(matchLength, utf8.RuneCountInString(token))
					}
				}
			}
		}
		if matchClass > bestClass || (matchClass == bestClass && matchLength > bestLength) {
			entity, docID = candidate, id
			bestClass, bestLength = matchClass, matchLength
		}
	}
	return entity, docID
}

func entityTokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

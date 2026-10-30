package chunker

import "unicode"

// MaxChunkBytes is the maximum UTF-8 text size of a chunk.
const MaxChunkBytes = 30000

// EstimateTokens estimates tokens from dense-script characters, words and runes.
func EstimateTokens(text string) int {
	return measureTokens(text).count()
}

type tokenEstimate struct {
	dense, words, other  int
	startsWord, endsWord bool
}

func measureTokens(text string) tokenEstimate {
	var estimate tokenEstimate
	for _, r := range text {
		estimate.addRune(r)
	}
	return estimate
}

func (e *tokenEstimate) addRune(r rune) {
	dense := unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana,
		unicode.Hangul, unicode.Thai, unicode.Lao, unicode.Khmer, unicode.Myanmar, unicode.Tibetan)
	word := !dense && !unicode.IsSpace(r)
	if e.dense+e.other == 0 {
		e.startsWord = word
	}
	if dense {
		e.dense++
	} else if word {
		e.other++
		if !e.endsWord {
			e.words++
		}
	}
	e.endsWord = word
}

func (e tokenEstimate) count() int {
	return e.dense + max((13*e.words+9)/10, (e.other+3)/4)
}

func (e tokenEstimate) append(next tokenEstimate, separated bool) tokenEstimate {
	if e.dense+e.other == 0 {
		return next
	}
	e.dense += next.dense
	e.other += next.other
	e.words += next.words
	if !separated && e.endsWord && next.startsWord {
		e.words--
	}
	e.endsWord = next.endsWord
	return e
}

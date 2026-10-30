package textutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizationPreservesSuperscriptsAndFractions(t *testing.T) {
	const input = "Area 10² m², growth 15%¹, ½ cup, ① item"
	assert.Equal(t, input, NormalizeForIngest(input))
}

func TestNormalizationFoldsWidthAndLigatures(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"width", "ＡＢＣ１２３ ｶﾀｶﾅ ﬁle", "ABC123 カタカナ file"},
		{"ligatures", "ﬀ ﬁ ﬂ ﬃ ﬄ ﬅ ﬆ", "ff fi fl ffi ffl st st"},
		{"spaces", "one\u00a0two\u2007three\u202ffour\u3000five", "one two three four five"},
		{"composition", "Cafe\u0301", "Café"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeForIngest(tt.input))
		})
	}
}

func TestInvisibleFormatCharactersAreRemoved(t *testing.T) {
	const input = "left\U000e0001\U000e0069\U000e0067\U000e006e\U000e006f\U000e0072\U000e0065\U000e007f\u2066\u2069\u00ad\u200b\ufeffright"
	assert.Equal(t, "leftright", NormalizeForIngest(input))
	assert.Equal(t, "left right", NormalizeForIngest("left\x00\x0c\x1f\x7f\u0085\u009fright"))
}

func TestCarriageReturnParagraphsArePreserved(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"carriage returns", "First line.\rSoft wrap.\r\rSecond paragraph."},
		{"windows", "First line.\r\nSoft wrap.\r\n\r\nSecond paragraph."},
		{"unicode separators", "First line.\u2028Soft wrap.\u2029Second paragraph."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, "First line. Soft wrap.\n\nSecond paragraph.", NormalizeForIngest(tt.input))
		})
	}
	assert.Equal(t, "documents", NormalizeForIngest("docu-\rments"))
}

func TestSanitizeForPrompt(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"format characters", "left\U000e0001\U000e0069\U000e007f\u2066\u2069\u00ad\u200b\ufeffright", "leftright"},
		{"controls", "A\x00\x01\x08\tB\nC\rD\x1f\x7f\u0085\u009fE", "A\tB\nCDE"},
		{"preserve text", "  10² ½ ① Ａ ﬁ <p>\n\nsoft-\nwrap\t ", "  10² ½ ① Ａ ﬁ <p>\n\nsoft-\nwrap\t "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SanitizeForPrompt(tt.input))
		})
	}
}

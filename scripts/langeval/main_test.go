package main

import "testing"

func TestLanguageMatchesQuestion(t *testing.T) {
	for _, tt := range []struct {
		language, answer string
		want             bool
	}{
		{"en", "The archive is available for research and provides the following documents.", true},
		{"es", "Los documentos del archivo contienen información sobre los sistemas disponibles.", true},
		{"en", "Los documentos del archivo contienen información sobre los sistemas disponibles.", false},
		{"es", "The archive is available for research and provides the following documents.", false},
		{"en", "", false},
	} {
		if got := languageMatches(tt.language, tt.answer); got != tt.want {
			t.Errorf("%s: %q got %v", tt.language, tt.answer, got)
		}
	}
}

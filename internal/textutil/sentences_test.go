package textutil

import (
	"reflect"
	"testing"
)

func TestSplitSentencesHandlesFullWidthTerminators(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{"unspaced", "一。二！三？四；五｡六", []string{"一。", "二！", "三？", "四；", "五｡", "六"}},
		{"punctuation runs", "本当？！ 次。", []string{"本当？！", "次。"}},
		{"existing boundaries", "First. Second!? Third… Last", []string{"First.", "Second!?", "Third…", "Last"}},
		{"ascii without whitespace", "first.second!third?last", []string{"first.second!third?last"}},
		{"mixed", "First. 中文。Next! Done", []string{"First.", "中文。", "Next!", "Done"}},
		{"empty", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SplitSentences(tc.text); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("sentences = %q, want %q", got, tc.want)
			}
		})
	}
}

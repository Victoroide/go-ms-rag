package chunker

import (
	"strings"
	"testing"
)

func TestLongSentenceIsBoundedWithoutLosingWords(t *testing.T) {
	text := strings.Repeat("preservation archive storage ", 100)
	chunks := NewChunker(20, 0).Chunk("document", text)
	if len(chunks) < 2 {
		t.Fatal("long sentence was not split")
	}
	var words []string
	for _, chunk := range chunks {
		if chunk.TokenCount > 20 {
			t.Fatalf("chunk exceeds budget: %d", chunk.TokenCount)
		}
		words = append(words, strings.Fields(chunk.Text)...)
	}
	if strings.Join(words, " ") != strings.Join(strings.Fields(text), " ") {
		t.Fatal("splitting lost or duplicated content")
	}
}
func TestChunkerEmptyAndExtremeWindows(t *testing.T) {
	if got := NewChunker(20, 4).Chunk("document", "<p>   </p>"); len(got) != 0 {
		t.Fatalf("empty markup produced chunks: %v", got)
	}
	for _, params := range [][2]int{{0, 0}, {1, 100}, {10, -3}} {
		chunks := NewChunker(params[0], params[1]).Chunk("document", "Text one. Text two. Text three.")
		if len(chunks) == 0 || len(chunks) > 20 {
			t.Fatalf("invalid progress for %v: %d chunks", params, len(chunks))
		}
		for _, chunk := range chunks {
			if chunk.Text == "" {
				t.Fatal("empty chunk")
			}
		}
	}
}

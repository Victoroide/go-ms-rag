package chunker

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Victoroide/quarry-rag/internal/domain"
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

func TestUnspacedScriptChunksStayWithinLimits(t *testing.T) {
	var source strings.Builder
	for i := 0; i < 57000; i++ {
		source.WriteRune(rune(0x4e00 + i%2000))
	}
	chunks := NewChunker(512, 0).Chunk("document", source.String())
	var restored strings.Builder
	for _, chunk := range chunks {
		if n := utf8.RuneCountInString(chunk.Text); n > 512 {
			t.Fatalf("chunk contains %d dense runes, exceeding 512 tokens", n)
		} else if chunk.TokenCount != n {
			t.Fatalf("token count = %d, want %d", chunk.TokenCount, n)
		}
		if len(chunk.Text) > 30000 || !utf8.ValidString(chunk.Text) {
			t.Fatalf("invalid chunk of %d bytes", len(chunk.Text))
		}
		restored.WriteString(chunk.Text)
	}
	if restored.String() != source.String() {
		t.Fatal("splitting lost, reordered or duplicated runes")
	}
}

func TestEstimateTokens(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"whitespace", " \t\n\u2003", 0},
		{"one word", "a", 2},
		{"round words up", "a b c", 4},
		{"ten words", "a b c d e f g h i j", 13},
		{"long word", "abcdefghijklmnopq", 5},
		{"four runes", "😀😀😀😀", 2},
		{"rune ceiling", "😀😀😀😀😀😀😀😀😀", 3},
		{"dense scripts", "漢あア한กກកကཀ", 9},
		{"dense separates words", "a漢bあc", 6},
		{"unicode whitespace", "a\u2003b", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EstimateTokens(tc.text); got != tc.want {
				t.Fatalf("tokens = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLongTokenIsSplit(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		size int
	}{
		{"base64 tokens", strings.Repeat("Abc123xy", 25000), 512},
		{"base64 bytes", strings.Repeat("Abc123xy", 25000), 8192},
		{"unicode bytes", strings.Repeat("😀", 50000), 8192},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks := NewChunker(tc.size, 0).Chunk("document", tc.text)
			if len(chunks) < 2 {
				t.Fatal("long token was not split")
			}
			var restored strings.Builder
			for _, chunk := range chunks {
				if len(chunk.Text) > 30000 || (utf8.RuneCountInString(chunk.Text)+3)/4 > tc.size || !utf8.ValidString(chunk.Text) {
					t.Fatalf("chunk exceeds limits or splits UTF-8: %d bytes", len(chunk.Text))
				}
				if got := EstimateTokens(chunk.Text); got > tc.size || chunk.TokenCount != got {
					t.Fatalf("token estimate = %d, stored count = %d, budget = %d", got, chunk.TokenCount, tc.size)
				}
				restored.WriteString(chunk.Text)
			}
			if restored.String() != tc.text {
				t.Fatal("splitting changed long token content")
			}
		})
	}
}

func TestNoChunkRepeatsOnlyOverlap(t *testing.T) {
	var words []string
	for i := 0; i < 620; i++ {
		word := fmt.Sprintf("w%03d", i)
		if i == 9 || i == 19 || i == 619 {
			word += "."
		}
		words = append(words, word)
	}
	chunks := NewChunker(512, 64).Chunk("document", strings.Join(words, " "))
	for i := 1; i < len(chunks); i++ {
		if strings.Contains(chunks[i-1].Text, chunks[i].Text) {
			t.Fatalf("chunk %d contains only the previous chunk's overlap", i)
		}
	}
	assertChunkWordOrder(t, chunks, words)
}

func TestFragmentsOfLongSentenceOverlap(t *testing.T) {
	var words []string
	for i := 0; i < 1000; i++ {
		word := fmt.Sprintf("word%04d", i)
		if i == 499 || i == 999 {
			word += "."
		}
		words = append(words, word)
	}
	chunks := NewChunker(100, 20).Chunk("document", strings.Join(words, " "))
	if len(chunks) < 2 {
		t.Fatal("long sentences were not split")
	}
	for i := 1; i < len(chunks); i++ {
		fragmentWords := strings.Fields(chunks[i].Text)
		shared := sharedWordCount(strings.Fields(chunks[i-1].Text), fragmentWords)
		if shared == 0 {
			t.Fatalf("fragments %d and %d have no overlap", i-1, i)
		}
		if got := EstimateTokens(strings.Join(fragmentWords[:shared], " ")); got > 20 {
			t.Fatalf("fragment overlap exceeds budget: %d", got)
		}
	}
	assertChunkWordOrder(t, chunks, words)
}

func TestChunkingPreservesWordOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(42)) // #nosec G404 -- deterministic test input
	for document := 0; document < 200; document++ {
		var source strings.Builder
		var words []string
		for i, n := 0, 10+rng.Intn(1000); i < n; i++ {
			word := fmt.Sprintf("w%04d%s", i, strings.Repeat("x", rng.Intn(20)))
			if rng.Intn(5) == 0 {
				word += "."
			}
			words = append(words, word)
			source.WriteString(word)
			if rng.Intn(8) == 0 {
				source.WriteString("\n\n")
			} else {
				source.WriteByte(' ')
			}
		}
		size := 32 + rng.Intn(200)
		chunks := NewChunker(size, rng.Intn(size)).Chunk("document", source.String())
		assertChunkWordOrder(t, chunks, words)
		for _, chunk := range chunks {
			if got := EstimateTokens(chunk.Text); got > size || chunk.TokenCount != got || len(chunk.Text) > 30000 {
				t.Fatalf("document %d exceeds chunk limits", document)
			}
		}
	}
}

func TestLongTokenFragmentsRetainLargeOverlap(t *testing.T) {
	var source strings.Builder
	for i := 0; i < 2000; i++ {
		source.WriteRune(rune(0x4e00 + i))
	}
	chunks := NewChunker(512, 500).Chunk("document", source.String())
	if len(chunks) < 2 {
		t.Fatal("long token was not split")
	}
	restored := chunks[0].Text
	for i := 1; i < len(chunks); i++ {
		previous, current := []rune(chunks[i-1].Text), []rune(chunks[i].Text)
		shared := 0
		for n := min(len(previous), len(current)); n > 0; n-- {
			if string(previous[len(previous)-n:]) == string(current[:n]) {
				shared = n
				break
			}
		}
		if shared == 0 || shared > 500 || shared == len(current) {
			t.Fatalf("invalid fragment overlap: %d runes", shared)
		}
		if len(current) > 512 {
			t.Fatal("fragment exceeds token budget")
		}
		restored += string(current[shared:])
	}
	if restored != source.String() {
		t.Fatal("overlapping fragments lost or reordered content")
	}
}

func assertChunkWordOrder(t *testing.T, chunks []domain.Chunk, want []string) {
	t.Helper()
	var restored, previous []string
	for _, chunk := range chunks {
		words := strings.Fields(chunk.Text)
		shared := sharedWordCount(previous, words)
		if shared == len(words) {
			t.Fatal("chunk contains no new words")
		}
		restored = append(restored, words[shared:]...)
		previous = words
	}
	if strings.Join(restored, " ") != strings.Join(want, " ") {
		t.Fatal("splitting lost, reordered or duplicated words")
	}
}

func sharedWordCount(previous, current []string) int {
	for n := min(len(previous), len(current)); n > 0; n-- {
		if strings.Join(previous[len(previous)-n:], " ") == strings.Join(current[:n], " ") {
			return n
		}
	}
	return 0
}

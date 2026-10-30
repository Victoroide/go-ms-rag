package chunker

import (
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/textutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlainTextComparisonsArePreserved(t *testing.T) {
	const measured = "Rule: if a < b then the result is valid. Many pages later the threshold x > y applies."
	for _, input := range []string{measured, "a < b && b > c", "Vector <T> and &amp; remain literal.", ""} {
		assert.Equal(t, input, stripHTML(input))
	}
	chunks := NewChunker(100, 0).Chunk(domain.DocumentID("comparison"), measured)
	require.Len(t, chunks, 1)
	assert.Equal(t, measured, chunks[0].Text)
}

func TestHTMLTagsScriptsAndEntities(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"elements", `<SPAN class="note">Fish &amp; chips &lt; 10 &gt; 2 &#189;</SPAN>`, "Fish & chips < 10 > 2 ½"},
		{"hidden content", "<p>Visible<!-- hidden --> text<script type=\"text/javascript\">\nsecret <b>code</b>\n</script ><style media=\"all\">\nhidden\n</style ></p>", "Visible text"},
		{"case", "<P>Visible<SCRIPT>secret</SCRIPT><STYLE>hidden</STYLE></P>", "Visible"},
		{"closing tag detection", "<widget>Text &amp; value</widget>", "Text & value"},
		{"comparisons in markup", "<p>Rule: a < b, x > y.</p>", "Rule: a < b, x > y."},
		{"line break", "<span>First<br />second</span>", "First second"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, textutil.NormalizeForIngest(stripHTML(tt.input)))
		})
	}
}

func TestBlockTagsBecomeParagraphBreaks(t *testing.T) {
	for _, tag := range []string{"p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "table", "ul", "ol", "section", "article", "header", "footer"} {
		t.Run(tag, func(t *testing.T) {
			input := "<" + tag + ">First.</" + tag + "><" + tag + ">Second.</" + tag + ">"
			assert.Equal(t, "First.\n\nSecond.", textutil.NormalizeForIngest(stripHTML(input)))
		})
	}
	assert.Equal(t, "First\nsecond", stripHTML("First<br>second"))
	chunks := NewChunker(100, 0).Chunk(domain.DocumentID("paragraphs"), "<p>First.</p><p>Second.</p>")
	require.Len(t, chunks, 1)
	assert.Equal(t, "First.\n\nSecond.", chunks[0].Text)
}

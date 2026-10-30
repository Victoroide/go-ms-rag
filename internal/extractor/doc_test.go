package extractor

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"golang.org/x/text/encoding/charmap"
)

func TestDOCRejectsStreamLargerThanContainer(t *testing.T) {
	size := uint32(268_435_456)
	data := buildCFB(t, []cfbStream{{name: "WordDocument", data: bytes.Repeat([]byte("x"), 512)}}, cfbOptions{declaredSize: &size, cyclic: true})
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), "WordDocument stream size exceeds the file size") {
		t.Errorf("expected an invalid stream size error, got %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= 16<<20 {
		t.Errorf("allocated %d bytes for a %d-byte file", allocated, len(data))
	}
}

func TestDOCRejectsCyclicChainWithinBudget(t *testing.T) {
	data := buildCFB(t, []cfbStream{{name: "WordDocument", data: bytes.Repeat([]byte("x"), 512)}}, cfbOptions{cyclic: true})
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	text, _ := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	runtime.ReadMemStats(&after)
	if len(text) > len(data) {
		t.Errorf("returned %d text bytes from a %d-byte file", len(text), len(data))
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= 16<<20 {
		t.Errorf("allocated %d bytes for a %d-byte file", allocated, len(data))
	}
}

func TestDOCExtractsMinimalDocument(t *testing.T) {
	data := docFixture(t, docCompressedPiece(t, "Document preservation requires stable storage."))
	text, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Document preservation requires stable storage." {
		t.Fatalf("unexpected text: %q", text)
	}
}

func TestDOCRejectsTextAboveContentLimit(t *testing.T) {
	data := docFixture(t, docCompressedPiece(t, strings.Repeat("x", domain.MaxContentBytes+1)))
	_, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if !errors.Is(err, ErrContentTooLarge) {
		t.Fatalf("expected the text limit, got %v", err)
	}
}

func TestDOCAcceptsTextAtContentLimit(t *testing.T) {
	data := docFixture(t, docCompressedPiece(t, strings.Repeat("x", domain.MaxContentBytes)))
	text, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(text) != domain.MaxContentBytes {
		t.Fatalf("returned %d text bytes", len(text))
	}
}

func TestDOCRejectsEmptyStream(t *testing.T) {
	size := uint32(0)
	data := buildCFB(t, []cfbStream{{name: "WordDocument"}}, cfbOptions{declaredSize: &size})
	_, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if err == nil || !strings.Contains(err.Error(), "WordDocument stream size exceeds the file size") {
		t.Fatalf("expected an invalid stream size error, got %v", err)
	}
}

type docPiece struct {
	data       []byte
	compressed bool
}

func docCompressedPiece(t *testing.T, text string) docPiece {
	t.Helper()
	data, err := charmap.Windows1252.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return docPiece{data: data, compressed: true}
}

func docUnicodePiece(text string) docPiece {
	units := utf16.Encode([]rune(text))
	data := make([]byte, 2*len(units))
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[2*i:], unit)
	}
	return docPiece{data: data}
}

func docFixtureSize(t *testing.T, size int) uint32 {
	t.Helper()
	if size < 0 || size > 7<<20 {
		t.Fatal("document fixture exceeds the container capacity")
		return 0
	}
	return uint32(size)
}

func docStreams(t *testing.T, pieces ...docPiece) (word, table []byte) {
	t.Helper()
	const textOffset = 0x0800
	word = make([]byte, textOffset)
	binary.LittleEndian.PutUint16(word, 0xA5EC)
	binary.LittleEndian.PutUint16(word[2:], 0x00C1)
	binary.LittleEndian.PutUint16(word[0x20:], 14)
	binary.LittleEndian.PutUint16(word[0x3E:], 22)
	binary.LittleEndian.PutUint16(word[0x98:], 93)
	binary.LittleEndian.PutUint32(word[0x1A6:], docFixtureSize(t, 9+12*len(pieces)))
	table = make([]byte, 9+12*len(pieces))
	table[0] = 2
	binary.LittleEndian.PutUint32(table[1:], docFixtureSize(t, len(table)-5))
	cp := uint32(0)
	for i, piece := range pieces {
		binary.LittleEndian.PutUint32(table[5+4*i:], cp)
		fc := docFixtureSize(t, len(word))
		if piece.compressed {
			fc = 0x40000000 | (2 * fc)
			cp += docFixtureSize(t, len(piece.data))
		} else {
			cp += docFixtureSize(t, len(piece.data)/2)
		}
		binary.LittleEndian.PutUint32(table[5+4*(len(pieces)+1)+8*i+2:], fc)
		word = append(word, piece.data...)
	}
	binary.LittleEndian.PutUint32(table[5+4*len(pieces):], cp)
	binary.LittleEndian.PutUint32(word[0x4C:], cp)
	word = append(word, make([]byte, max(0, 4096-len(word)))...)
	return word, table
}

func docFixture(t *testing.T, pieces ...docPiece) []byte {
	t.Helper()
	word, table := docStreams(t, pieces...)
	return buildCFB(t, []cfbStream{{name: "WordDocument", data: word}, {name: "0Table", data: table}}, cfbOptions{})
}

func TestDOCCompressedPieceWithAccents(t *testing.T) {
	want := "Educación, años, naïve — €"
	data := docFixture(t, docCompressedPiece(t, want))
	got, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestDOCUnicodePiece(t *testing.T) {
	want := "日本語テキスト 😀"
	data := docFixture(t, docUnicodePiece(want))
	got, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestDOCMixedPieces(t *testing.T) {
	word, table := docStreams(t, docCompressedPiece(t, "Educación: "), docUnicodePiece("日本語"), docCompressedPiece(t, "!"))
	copy(word[0x600:], "Times New Roman Normal metadata noise")
	data := buildCFB(t, []cfbStream{{name: "WordDocument", data: word}, {name: "0Table", data: table}}, cfbOptions{})
	got, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if err != nil || got != "Educación: 日本語!" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDOCFieldCodesAreDropped(t *testing.T) {
	data := docFixture(t,
		docCompressedPiece(t, "Before \x13 OUTER \x13 INNER \x14hidden\x15 \x14visible "),
		docUnicodePiece("\x13 NESTED \x14日本語\x15\x15 after\rline\vnext\fpage\x07cell\x1eend\x1f\x01\x08\x02done"),
	)
	want := "Before visible 日本語 after\nline\nnext\n\npage\tcell-end done"
	got, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestDOCEncryptedIsRejected(t *testing.T) {
	word, table := docStreams(t, docCompressedPiece(t, "Secret document content."))
	binary.LittleEndian.PutUint16(word[0x0A:], 0x0100)
	data := buildCFB(t, []cfbStream{{name: "WordDocument", data: word}, {name: "0Table", data: table}}, cfbOptions{})
	_, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if !errors.Is(err, ErrEncryptedDocument) || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("expected an encrypted document error, got %v", err)
	}
}

func TestDOCWord95IsRejected(t *testing.T) {
	word, table := docStreams(t, docCompressedPiece(t, "An older document format."))
	binary.LittleEndian.PutUint16(word[2:], 0x0068)
	data := buildCFB(t, []cfbStream{{name: "WordDocument", data: word}, {name: "0Table", data: table}}, cfbOptions{})
	_, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if !errors.Is(err, ErrUnsupportedDocumentVersion) || !strings.Contains(err.Error(), "unsupported document version") {
		t.Fatalf("expected an unsupported version error, got %v", err)
	}
}

func TestDOCCorruptPieceTableIsRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(word, table []byte)
	}{
		{"signature", func(w, _ []byte) { binary.LittleEndian.PutUint16(w, 0) }},
		{"word count", func(w, _ []byte) { binary.LittleEndian.PutUint16(w[0x20:], 0xFFFF) }},
		{"long count", func(w, _ []byte) { binary.LittleEndian.PutUint16(w[0x3E:], 0xFFFF) }},
		{"missing text count", func(w, _ []byte) { binary.LittleEndian.PutUint16(w[0x3E:], 3) }},
		{"negative text count", func(w, _ []byte) { binary.LittleEndian.PutUint32(w[0x4C:], 0xFFFFFFFF) }},
		{"missing clx pair", func(w, _ []byte) { binary.LittleEndian.PutUint16(w[0x98:], 33) }},
		{"blob count", func(w, _ []byte) { binary.LittleEndian.PutUint16(w[0x98:], 0xFFFF) }},
		{"clx offset", func(w, _ []byte) { binary.LittleEndian.PutUint32(w[0x1A2:], 0xFFFFFFFF) }},
		{"clx length", func(w, _ []byte) { binary.LittleEndian.PutUint32(w[0x1A6:], 0xFFFFFFFF) }},
		{"clx type", func(_, tb []byte) { tb[0] = 3 }},
		{"short prc", func(w, tb []byte) { tb[0] = 1; binary.LittleEndian.PutUint32(w[0x1A6:], 2) }},
		{"negative prc", func(_, tb []byte) { tb[0] = 1; binary.LittleEndian.PutUint16(tb[1:], 0xFFFF) }},
		{"short pcdt", func(w, _ []byte) { binary.LittleEndian.PutUint32(w[0x1A6:], 4) }},
		{"pcdt length", func(_, tb []byte) { binary.LittleEndian.PutUint32(tb[1:], 0xFFFFFFFF) }},
		{"empty pieces", func(_, tb []byte) { binary.LittleEndian.PutUint32(tb[1:], 4) }},
		{"misaligned pieces", func(_, tb []byte) { binary.LittleEndian.PutUint32(tb[1:], 15) }},
		{"initial cp", func(_, tb []byte) { binary.LittleEndian.PutUint32(tb[5:], 1) }},
		{"negative cp", func(_, tb []byte) { binary.LittleEndian.PutUint32(tb[9:], 0xFFFFFFFF) }},
		{"decreasing cp", func(_, tb []byte) { binary.LittleEndian.PutUint32(tb[13:], 1) }},
		{"short main story", func(w, _ []byte) { binary.LittleEndian.PutUint32(w[0x4C:], 100) }},
		{"piece offset", func(_, tb []byte) { binary.LittleEndian.PutUint32(tb[19:], 0x3FFFFFFF) }},
		{"piece end", func(_, tb []byte) { binary.LittleEndian.PutUint32(tb[19:], 4094) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			word, table := docStreams(t, docCompressedPiece(t, "first piece"), docUnicodePiece("日本語"))
			tc.mutate(word, table)
			data := buildCFB(t, []cfbStream{{name: "WordDocument", data: word}, {name: "0Table", data: table}}, cfbOptions{})
			_, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
			if err == nil || !strings.Contains(err.Error(), "unsupported or corrupt .doc file") {
				t.Fatalf("expected a corrupt document error, got %v", err)
			}
		})
	}
}

func TestDOCPieceTextAboveLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pieces []docPiece
	}{
		{"decoded bytes", []docPiece{docCompressedPiece(t, strings.Repeat("é", domain.MaxContentBytes/2+1))}},
		{"running total", []docPiece{docCompressedPiece(t, strings.Repeat("x", domain.MaxContentBytes/2)), docUnicodePiece(strings.Repeat("界", domain.MaxContentBytes/6+1))}},
		{"hidden instructions", []docPiece{docCompressedPiece(t, "\x13"+strings.Repeat("x", domain.MaxContentBytes)+"\x15visible")}},
		{"expanded controls", []docPiece{docCompressedPiece(t, strings.Repeat("\x0c", domain.MaxContentBytes/2+1))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := docFixture(t, tc.pieces...)
			_, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
			if !errors.Is(err, ErrContentTooLarge) {
				t.Fatalf("expected the text limit, got %v", err)
			}
		})
	}
}

func TestDOCSelectedTableAndProperties(t *testing.T) {
	word, table := docStreams(t, docCompressedPiece(t, "Selected table content."))
	copy(word[0x600:], "Binary metadata noise")
	binary.LittleEndian.PutUint16(word[0x0A:], 0x0200)
	table = append([]byte{1, 2, 0, 0xAA, 0xBB}, table...)
	binary.LittleEndian.PutUint32(word[0x1A6:], docFixtureSize(t, len(table)))
	data := buildCFB(t, []cfbStream{{name: "1Table", data: table}, {name: "WordDocument", data: word}, {name: "0Table", data: []byte("unselected table noise")}}, cfbOptions{})
	got, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if err != nil || got != "Selected table content." {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDOCFIBVariableCounts(t *testing.T) {
	word, table := docStreams(t, docCompressedPiece(t, "Variable field counts."))
	copy(word[0xA0:0xA0+93*8], word[0x9A:0x9A+93*8])
	binary.LittleEndian.PutUint16(word[0x20:], 15)
	binary.LittleEndian.PutUint16(word[0x40:], 23)
	binary.LittleEndian.PutUint32(word[0x4E:], uint32(len("Variable field counts.")))
	binary.LittleEndian.PutUint16(word[0x9E:], 93)
	data := buildCFB(t, []cfbStream{{name: "WordDocument", data: word}, {name: "0Table", data: table}}, cfbOptions{})
	got, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if err != nil || got != "Variable field counts." {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDOCRejectsTableStreamLargerThanContainer(t *testing.T) {
	word, table := docStreams(t, docCompressedPiece(t, "Document text."))
	data := buildCFB(t, []cfbStream{{name: "WordDocument", data: word}, {name: "0Table", data: table}}, cfbOptions{})
	directory := (int(binary.LittleEndian.Uint32(data[48:])) + 1) * 512
	binary.LittleEndian.PutUint32(data[directory+2*128+120:], 268_435_456)
	_, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if err == nil || !strings.Contains(err.Error(), "0Table stream size exceeds the file size") {
		t.Fatalf("expected an invalid table stream size error, got %v", err)
	}
}

func TestDOCMainStoryOnly(t *testing.T) {
	word, table := docStreams(t, docUnicodePiece("本文"), docCompressedPiece(t, "Header and footnote text."))
	binary.LittleEndian.PutUint32(word[0x4C:], 2)
	data := buildCFB(t, []cfbStream{{name: "WordDocument", data: word}, {name: "0Table", data: table}}, cfbOptions{})
	got, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if err != nil || got != "本文" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDOCNoReadableText(t *testing.T) {
	data := docFixture(t, docCompressedPiece(t, "\x13instructions only\x15\r\x01"))
	_, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if err == nil || !strings.Contains(err.Error(), "no readable text") {
		t.Fatalf("expected no readable text, got %v", err)
	}
}

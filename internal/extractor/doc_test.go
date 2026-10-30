package extractor

import (
	"bytes"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
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
	data := buildCFB(t, []cfbStream{{name: "WordDocument", data: []byte("Document preservation requires stable storage.")}}, cfbOptions{})
	text, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Document preservation requires stable storage." {
		t.Fatalf("unexpected text: %q", text)
	}
}

func TestDOCRejectsTextAboveContentLimit(t *testing.T) {
	data := buildCFB(t, []cfbStream{{name: "WordDocument", data: bytes.Repeat([]byte("x"), domain.MaxContentBytes+1)}}, cfbOptions{})
	_, err := New(FileTypeDOC).Extract(t.Context(), bytes.NewReader(data))
	if !errors.Is(err, ErrContentTooLarge) {
		t.Fatalf("expected the text limit, got %v", err)
	}
}

func TestDOCAcceptsTextAtContentLimit(t *testing.T) {
	data := buildCFB(t, []cfbStream{{name: "WordDocument", data: bytes.Repeat([]byte("x"), domain.MaxContentBytes)}}, cfbOptions{})
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

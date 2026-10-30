package extractor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
)

func TestSniffDetectsTypesWithoutExtension(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want FileType
	}{
		{"pdf", []byte("%PDF-1.7\n"), FileTypePDF},
		{"pdf prefix", append(bytes.Repeat([]byte{0}, 100), []byte("%PDF-1.7")...), FileTypePDF},
		{"docx", docxPartsBytes(t, map[string]string{"word/document.xml": "document"}), FileTypeDOCX},
		{"relationship", docxPartsBytes(t, map[string]string{"_rels/.rels": `<Relationships><Relationship Type="office/officeDocument" Target="custom/main.xml"/></Relationships>`, "custom/main.xml": "document"}), FileTypeDOCX},
		{"relationship missing part", docxPartsBytes(t, map[string]string{"_rels/.rels": `<Relationships><Relationship Type="office/officeDocument" Target="word/document.xml"/></Relationships>`}), FileTypeDOCX},
		{"doc", buildCFB(t, []cfbStream{{name: "WordDocument", data: []byte("document")}}, cfbOptions{}), FileTypeDOC},
		{"utf8", []byte("Plain text 世界 🌍"), FileTypeText},
		{"utf8 sample boundary", []byte(strings.Repeat("x", 8191) + "世界"), FileTypeText},
		{"utf16le", []byte{0xff, 0xfe, 'a', 0}, FileTypeText},
		{"utf16be", []byte{0xfe, 0xff, 0, 'a'}, FileTypeText},
		{"empty", nil, ""},
		{"nul", []byte("binary\x00file"), ""},
		{"invalid utf8", []byte{0xff, 0x80}, ""},
		{"invalid utf8 at sample boundary", append(bytes.Repeat([]byte{'a'}, 8191), 0xff, 0x80), ""},
		{"other zip", docxPartsBytes(t, map[string]string{"other.xml": "document"}), ""},
		{"other compound", buildCFB(t, []cfbStream{{name: "Workbook", data: []byte("sheet")}}, cfbOptions{}), ""},
		{"truncated zip", []byte("PK\x03\x04"), ""},
		{"truncated compound", []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, ""},
		{"pdf beyond prefix", append(bytes.Repeat([]byte{0}, 1024), []byte("%PDF-1.7")...), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sniff(tt.data); got != tt.want {
				t.Fatalf("type=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveReconcilesExtensionAndContent(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want FileType
		err  error
	}{
		{"file.PDF", []byte("%PDF-1.4"), FileTypePDF, nil},
		{"file", []byte("%PDF-1.4"), FileTypePDF, nil},
		{"file.bin", []byte("plain text"), FileTypeText, nil},
		{"file.txt", []byte("plain text"), FileTypeText, nil},
		{"file.md", []byte("plain text"), FileTypeText, nil},
		{"file.markdown", []byte("plain text"), FileTypeText, nil},
		{"file.docx", []byte("%PDF-1.4"), "", ErrTypeMismatch},
		{"file.pdf", []byte("plain text"), "", ErrTypeMismatch},
		{"file.txt", []byte{0, 0xff}, "", ErrUnsupportedType},
		{"file.pdf", []byte{0, 0xff}, "", ErrUnsupportedType},
		{"file", []byte{0, 0xff}, "", ErrUnsupportedType},
	} {
		t.Run(tt.name+string(tt.data), func(t *testing.T) {
			got, err := Resolve(tt.name, tt.data)
			if got != tt.want || !errors.Is(err, tt.err) {
				t.Fatalf("type=%q err=%v; want %q %v", got, err, tt.want, tt.err)
			}
		})
	}
}

func TestTextExtractorNormalizesEncoding(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want string
	}{
		{"utf8 bom", []byte("\xef\xbb\xbf  Readable plain text  "), "Readable plain text"},
		{"invalid utf8 and nul", []byte("Readable\x00 text \xff"), "Readable text �"},
		{"utf16le", []byte{0xff, 0xfe, 'H', 0, 'e', 0, 'l', 0, 'l', 0, 'o', 0, ' ', 0, 'w', 0, 'o', 0, 'r', 0, 'l', 0, 'd', 0}, "Hello world"},
		{"utf16be", []byte{0xfe, 0xff, 0, 'H', 0, 'e', 0, 'l', 0, 'l', 0, 'o', 0, ' ', 0, 'w', 0, 'o', 0, 'r', 0, 'l', 0, 'd'}, "Hello world"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New(FileTypeText).Extract(t.Context(), bytes.NewReader(tt.data))
			if err != nil || got != tt.want {
				t.Fatalf("text=%q err=%v; want %q", got, err, tt.want)
			}
		})
	}
	if got := ContentTypeFromFileType(FileTypeText); got != "text/plain; charset=utf-8" {
		t.Fatalf("content type=%q", got)
	}
}

func TestEmptyExtractedTextUsesSentinel(t *testing.T) {
	_, err := New(FileTypeText).Extract(t.Context(), strings.NewReader(" "))
	if !errors.Is(err, ErrNoText) {
		t.Fatalf("empty text: %v", err)
	}
	_, err = New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(docxPartsBytes(t, map[string]string{"word/document.xml": `<document/>`})))
	if !errors.Is(err, ErrNoText) {
		t.Fatalf("empty docx: %v", err)
	}
	_, err = cleanDOCText(" ")
	if !errors.Is(err, ErrNoText) {
		t.Fatalf("empty doc: %v", err)
	}
}

func TestTextLimitAppliesAfterDecoding(t *testing.T) {
	data := append([]byte{0xff, 0xfe}, bytes.Repeat([]byte{0x4c, 0x75}, domain.MaxContentBytes/3+1)...)
	_, err := New(FileTypeText).Extract(t.Context(), bytes.NewReader(data))
	if !errors.Is(err, ErrContentTooLarge) {
		t.Fatalf("decoded text limit: %v", err)
	}
}

func TestSniffRejectsMalformedCompoundDirectory(t *testing.T) {
	data := buildCFB(t, []cfbStream{{name: "WordDocument"}}, cfbOptions{})
	binary.LittleEndian.PutUint32(data[48:], 0xfffffffe)
	if got := Sniff(data); got != "" {
		t.Fatalf("malformed compound type=%q", got)
	}
}

func TestSniffRequiresWordDocumentStream(t *testing.T) {
	data := buildCFB(t, []cfbStream{{name: "WordDocument"}}, cfbOptions{})
	data[2*512+128+66] = 1
	if got := Sniff(data); got != "" {
		t.Fatalf("storage entry identified as %q", got)
	}
}

func TestResolveContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ResolveContext(ctx, "notes.txt", []byte("Plain text contents")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled detection: %v", err)
	}
}

package extractor

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
)

func docxBytes(t *testing.T, xml string) []byte {
	t.Helper()
	return docxPartsBytes(t, map[string]string{"word/document.xml": xml})
}

func docxPartsBytes(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for name, content := range parts {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestDOCXTextBoxKeepsSurroundingText(t *testing.T) {
	data := docxBytes(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Before the box </w:t><w:drawing><w:txbxContent><w:p><w:r><w:t>Inside the box.</w:t></w:r></w:p></w:txbxContent></w:drawing><w:t>after the box.</w:t></w:r></w:p></w:body></w:document>`)
	got, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Inside the box.\nBefore the box after the box." {
		t.Fatalf("nested paragraph lost text: %q", got)
	}
}

func TestDOCXAlternateContentIsNotDuplicated(t *testing.T) {
	data := docxBytes(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"><w:body><mc:AlternateContent><mc:Choice Requires="w"><w:p><w:r><w:t>One copy of this text.</w:t></w:r></w:p></mc:Choice><mc:Fallback><w:txbxContent><w:p><w:r><w:t>One copy of this text.</w:t></w:r></w:p></w:txbxContent></mc:Fallback></mc:AlternateContent><w:p><w:r><w:t>Text after fallback.</w:t></w:r></w:p></w:body></w:document>`)
	got, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got != "One copy of this text.\nText after fallback." {
		t.Fatalf("alternate content changed text: %q", got)
	}
}

func TestDOCXStrictNamespace(t *testing.T) {
	data := docxBytes(t, `<s:document xmlns:s="http://purl.oclc.org/ooxml/wordprocessingml/main"><s:body><s:p><s:r><s:t>Strict formatting</s:t><s:tab/><s:t>keeps text</s:t><s:br/><s:t>and line breaks.</s:t></s:r></s:p></s:body></s:document>`)
	got, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Strict formatting\tkeeps text\nand line breaks." {
		t.Fatalf("strict text changed: %q", got)
	}
}

func TestDOCXFootnotesAreIncluded(t *testing.T) {
	data := docxPartsBytes(t, map[string]string{
		"word/document.xml":  `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Main document text.</w:t></w:r></w:p></w:body></w:document>`,
		"word/footnotes.xml": `<w:footnotes xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:footnote w:type="separator"><w:p><w:r><w:t>Omit separator text.</w:t></w:r></w:p></w:footnote><w:footnote w:type="continuationSeparator"><w:p><w:r><w:t>Omit continuation text.</w:t></w:r></w:p></w:footnote><w:footnote w:id="1"><w:p><w:r><w:t>Footnote text.</w:t></w:r></w:p></w:footnote></w:footnotes>`,
		"word/endnotes.xml":  `<w:endnotes xmlns:w="http://purl.oclc.org/ooxml/wordprocessingml/main"><w:endnote w:type="separator"><w:p><w:r><w:t>Omit end separator.</w:t></w:r></w:p></w:endnote><w:endnote w:type="continuationSeparator"><w:p><w:r><w:t>Omit end continuation.</w:t></w:r></w:p></w:endnote><w:endnote w:type="normal" w:id="2"><w:p><w:r><w:t>Endnote text.</w:t></w:r></w:p></w:endnote></w:endnotes>`,
		"word/header1.xml":   `<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t>Omit header text.</w:t></w:r></w:p></w:hdr>`,
		"word/footer1.xml":   `<w:ftr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t>Omit footer text.</w:t></w:r></w:p></w:ftr>`,
		"word/comments.xml":  `<w:comments xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:comment><w:p><w:r><w:t>Omit comment text.</w:t></w:r></w:p></w:comment></w:comments>`,
	})
	got, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Main document text.\nFootnote text.\nEndnote text." {
		t.Fatalf("notes changed text: %q", got)
	}
}

func TestDOCXMainPartFromRelationships(t *testing.T) {
	for _, target := range []string{"custom/main.xml", "/custom/main.xml", "./custom/main.xml"} {
		t.Run(target, func(t *testing.T) {
			data := docxPartsBytes(t, map[string]string{
				"_rels/.rels":       `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="other" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/><Relationship Id="main" Type="http://purl.oclc.org/ooxml/officeDocument/relationships/officeDocument" Target="` + target + `"/></Relationships>`,
				"custom/main.xml":   `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Relationship selected text.</w:t></w:r></w:p></w:body></w:document>`,
				"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Wrong main document.</w:t></w:r></w:p></w:body></w:document>`,
			})
			got, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if got != "Relationship selected text." {
				t.Fatalf("wrong main part: %q", got)
			}
		})
	}
}

func TestDOCXHyphens(t *testing.T) {
	data := docxBytes(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Non</w:t><w:noBreakHyphen/><w:t>breaking and soft</w:t><w:softHyphen/><w:t>hyphen text.</w:t></w:r></w:p></w:body></w:document>`)
	got, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Non-breaking and softhyphen text." {
		t.Fatalf("hyphens changed text: %q", got)
	}
}

func TestDOCXNotesShareTextLimit(t *testing.T) {
	for notePart, noteKind := range map[string]string{"word/footnotes.xml": "footnote", "word/endnotes.xml": "endnote"} {
		t.Run(notePart, func(t *testing.T) {
			for _, size := range []int{domain.MaxContentBytes - 12, domain.MaxContentBytes - 11, domain.MaxContentBytes - 10, domain.MaxContentBytes + 1} {
				data := docxPartsBytes(t, map[string]string{
					"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Body text.</w:t></w:r></w:p></w:body></w:document>`,
					notePart:            `<w:` + noteKind + `s xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:` + noteKind + `><w:p><w:r><w:t>` + strings.Repeat("n", size) + `</w:t></w:r></w:p></w:` + noteKind + `></w:` + noteKind + `s>`,
				})
				got, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
				if size+11 > domain.MaxContentBytes {
					if !errors.Is(err, ErrContentTooLarge) {
						t.Fatalf("%d note bytes: expected text limit, got %v", size, err)
					}
				} else if err != nil || len(got) != size+11 {
					t.Fatalf("%d note bytes: got %d text bytes, error %v", size, len(got), err)
				}
			}
		})
	}
}

func TestDOCXAllPartsShareTextLimit(t *testing.T) {
	data := docxPartsBytes(t, map[string]string{
		"word/document.xml":  `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Body text.</w:t></w:r></w:p></w:body></w:document>`,
		"word/footnotes.xml": `<w:footnotes xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:footnote><w:p><w:r><w:t>` + strings.Repeat("f", domain.MaxContentBytes/2) + `</w:t></w:r></w:p></w:footnote></w:footnotes>`,
		"word/endnotes.xml":  `<w:endnotes xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:endnote><w:p><w:r><w:t>` + strings.Repeat("e", domain.MaxContentBytes/2) + `</w:t></w:r></w:p></w:endnote></w:endnotes>`,
	})
	if _, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data)); !errors.Is(err, ErrContentTooLarge) {
		t.Fatalf("expected combined text limit, got %v", err)
	}
}

func TestDOCXNotesKeepXMLLimit(t *testing.T) {
	for notePart, noteKind := range map[string]string{"word/footnotes.xml": "footnote", "word/endnotes.xml": "endnote"} {
		t.Run(notePart, func(t *testing.T) {
			data := docxPartsBytes(t, map[string]string{
				"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Main document text.</w:t></w:r></w:p></w:body></w:document>`,
				notePart:            `<w:` + noteKind + `s xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` + strings.Repeat(" ", 16<<20) + `</w:` + noteKind + `s>`,
			})
			if _, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data)); err == nil {
				t.Fatal("oversized note XML accepted")
			}
		})
	}
}

func TestDOCXRejectsOversizedRelationships(t *testing.T) {
	data := docxPartsBytes(t, map[string]string{
		"_rels/.rels":       `<Relationships>` + strings.Repeat(" ", 1<<20) + `</Relationships>`,
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Main document text.</w:t></w:r></w:p></w:body></w:document>`,
	})
	if _, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data)); err == nil {
		t.Fatal("oversized relationships accepted")
	}
}
func TestDOCXPreservesTextAcrossFormattingRuns(t *testing.T) {
	data := docxBytes(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Document pre</w:t></w:r><w:r><w:t>servation requires </w:t></w:r><w:r><w:t>stable storage.</w:t></w:r></w:p></w:body></w:document>`)
	got, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Document preservation requires stable storage." {
		t.Fatalf("formatting changed text: %q", got)
	}
}
func TestExtractorsHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, format := range []FileType{FileTypeText, FileTypeDOC, FileTypeDOCX, FileTypePDF} {
		_, err := New(format).Extract(ctx, strings.NewReader("Sufficiently long source text."))
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s: got %v want cancellation", format, err)
		}
	}
}
func TestDOCXRejectsExcessiveExpansion(t *testing.T) {
	data := docxBytes(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>`+strings.Repeat("x", (16<<20)+1)+`</w:t></w:r></w:p></w:body></w:document>`)
	_, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
	if err == nil {
		t.Fatal("oversized document XML accepted")
	}
	if !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDOCXRejectsTextAboveContentLimit(t *testing.T) {
	for name, paragraphs := range map[string]string{
		"single run":           `<w:p><w:r><w:t>` + strings.Repeat("x", domain.MaxContentBytes+1) + `</w:t></w:r></w:p>`,
		"formatting runs":      `<w:p>` + strings.Repeat(`<w:r><w:t>`+strings.Repeat("x", 1000)+`</w:t></w:r>`, 1001) + `</w:p>`,
		"paragraph separators": strings.Repeat(`<w:p><w:r><w:t>`+strings.Repeat("x", 1000)+`</w:t></w:r></w:p>`, 1000),
		"tabs":                 `<w:p><w:r><w:t>` + strings.Repeat("x", domain.MaxContentBytes-1) + `</w:t><w:tab/><w:t>x</w:t></w:r></w:p>`,
		"breaks":               `<w:p><w:r><w:t>` + strings.Repeat("x", domain.MaxContentBytes-1) + `</w:t><w:br/><w:t>x</w:t></w:r></w:p>`,
		"carriage returns":     `<w:p><w:r><w:t>` + strings.Repeat("x", domain.MaxContentBytes-1) + `</w:t><w:cr/><w:t>x</w:t></w:r></w:p>`,
		"unicode":              `<w:p><w:r><w:t>` + strings.Repeat("界", domain.MaxContentBytes/3+1) + `</w:t></w:r></w:p>`,
	} {
		t.Run(name, func(t *testing.T) {
			data := docxBytes(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`+paragraphs+`</w:body></w:document>`)
			_, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
			if !errors.Is(err, ErrContentTooLarge) {
				t.Fatalf("expected the text limit, got %v", err)
			}
		})
	}
}

func TestDOCXAcceptsTextAtContentLimit(t *testing.T) {
	data := docxBytes(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>`+strings.Repeat("x", domain.MaxContentBytes)+`</w:t></w:r></w:p></w:body></w:document>`)
	text, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(text) != domain.MaxContentBytes {
		t.Fatalf("returned %d text bytes", len(text))
	}
}

func TestDOCXStopsAtTextLimitBeforeMalformedXML(t *testing.T) {
	data := docxBytes(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>`+strings.Repeat("x", domain.MaxContentBytes+1)+`</w:t></w:r></w:p><`)
	_, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
	if !errors.Is(err, ErrContentTooLarge) {
		t.Fatalf("expected the text limit before the XML error, got %v", err)
	}
}

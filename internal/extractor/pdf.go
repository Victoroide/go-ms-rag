package extractor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Victoroide/go-ms-rag/internal/domain"

	"github.com/ledongthuc/pdf"
)

type pdfExtractor struct{}

func (e *pdfExtractor) Type() FileType { return FileTypePDF }

var multiBlankLine = regexp.MustCompile(`\n{3,}`)

func (e *pdfExtractor) Extract(ctx context.Context, r io.Reader) (result string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			result = ""
			err = fmt.Errorf("extractor: pdf: invalid document: %v", recovered)
		}
	}()

	data, err := readSource(ctx, r)
	if err != nil {
		return "", fmt.Errorf("extractor: pdf: read: %w", err)
	}

	reader, err := pdf.NewReader(&pdfSource{ctx: ctx, reader: bytes.NewReader(data), remaining: 64 << 20}, int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("extractor: pdf: %w", err)
	}

	var buf strings.Builder
	numPages := reader.NumPage()
	if numPages > 1000 {
		return "", fmt.Errorf("extractor: pdf: maximum page count exceeded")
	}
	decodedBudget := int64(8 << 20)

	for i := 1; i <= numPages; i++ {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("extractor: pdf: %w", err)
		}
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}
		// Check expanded streams before the parser allocates text and font maps.
		if streamErr := checkPDFStream(ctx, page.V.Key("Contents"), &decodedBudget, 0); streamErr != nil {
			return "", streamErr
		}
		fonts := page.Fonts()
		if len(fonts) > 128 {
			return "", fmt.Errorf("extractor: pdf: maximum font count exceeded")
		}
		for _, name := range fonts {
			font := page.Font(name)
			if font.V.Key("Encoding").Key("Differences").Len() > 512 {
				return "", fmt.Errorf("extractor: pdf: maximum font map size exceeded")
			}
			if streamErr := checkPDFFontMap(ctx, font.V.Key("ToUnicode"), &decodedBudget); streamErr != nil {
				return "", streamErr
			}
		}
		text, pErr := page.GetPlainText(nil)
		if contextErr := ctx.Err(); contextErr != nil {
			return "", fmt.Errorf("extractor: pdf: %w", contextErr)
		}

		if pErr != nil {
			continue
		}
		cleaned := stripControlChars(text)
		cleaned = strings.TrimSpace(cleaned)
		if cleaned == "" {
			continue
		}
		separator := 0
		if buf.Len() > 0 {
			separator = 2
		}
		if len(cleaned) > domain.MaxContentBytes-buf.Len()-separator {
			return "", fmt.Errorf("extractor: pdf: extracted text exceeds maximum size of %d bytes", domain.MaxContentBytes)
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(cleaned)
	}

	result = multiBlankLine.ReplaceAllString(buf.String(), "\n\n")
	result = strings.TrimSpace(result)
	if len(result) < 10 {
		return "", fmt.Errorf("extractor: pdf: no readable text found")
	}
	return result, nil
}

func stripControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r != 0x7F) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Stream and source budgets also bound repeated object reads in malformed files.
type pdfSource struct {
	ctx       context.Context
	reader    io.ReaderAt
	remaining int64
}

func (s *pdfSource) ReadAt(p []byte, offset int64) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > s.remaining {
		return 0, fmt.Errorf("extractor: pdf: maximum source read budget exceeded")
	}
	s.remaining -= int64(len(p))
	return s.reader.ReadAt(p, offset)
}

func checkPDFStream(ctx context.Context, value pdf.Value, remaining *int64, depth int) error {
	if depth > 64 {
		return fmt.Errorf("extractor: pdf: maximum stream nesting exceeded")
	}
	switch value.Kind() {
	case pdf.Array:
		if value.Len() > 4096 {
			return fmt.Errorf("extractor: pdf: maximum stream count exceeded")
		}
		for i := 0; i < value.Len(); i++ {
			if err := checkPDFStream(ctx, value.Index(i), remaining, depth+1); err != nil {
				return err
			}
		}
	case pdf.Stream:
		reader := value.Reader()
		count, err := io.Copy(io.Discard, io.LimitReader(contextReader{ctx: ctx, reader: reader}, *remaining+1))
		closeErr := reader.Close()
		if err != nil {
			return fmt.Errorf("extractor: pdf: decode stream: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("extractor: pdf: close stream: %w", closeErr)
		}
		*remaining -= count
		if *remaining < 0 {
			return fmt.Errorf("extractor: pdf: maximum decoded stream size exceeded")
		}
	}
	return nil
}

var (
	fontMapCounts = regexp.MustCompile(`(\S+)\s+begin(?:bfchar|bfrange|codespacerange)\b`)
	fontMapHex    = regexp.MustCompile(`<([[:xdigit:][:space:]]*)>`)
)

// CMap declarations can request far more work than their small streams suggest.
func checkPDFFontMap(ctx context.Context, value pdf.Value, remaining *int64) error {
	if value.Kind() != pdf.Stream {
		return nil
	}
	reader := value.Reader()
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: reader}, min(*remaining, 256<<10)+1))
	closeErr := reader.Close()
	if err != nil {
		return fmt.Errorf("extractor: pdf: read font map: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("extractor: pdf: close font map: %w", closeErr)
	}
	*remaining -= int64(len(data))
	if len(data) > 256<<10 || *remaining < 0 {
		return fmt.Errorf("extractor: pdf: maximum font map size exceeded")
	}
	entries := 0
	for _, match := range fontMapCounts.FindAllSubmatch(data, -1) {
		count, parseErr := strconv.Atoi(string(match[1]))
		if parseErr != nil || count < 0 || count > 4096-entries {
			return fmt.Errorf("extractor: pdf: maximum font map work exceeded")
		}
		entries += count
	}
	for _, match := range fontMapHex.FindAllSubmatch(data, -1) {
		digits := 0
		for _, digit := range string(match[1]) {
			if !unicode.IsSpace(digit) {
				digits++
			}
		}
		if digits > 16 {
			return fmt.Errorf("extractor: pdf: maximum font map character size exceeded")
		}
	}
	return nil
}

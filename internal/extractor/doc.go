package extractor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/richardlehane/mscfb"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

// ErrEncryptedDocument indicates that the document requires decryption.
var ErrEncryptedDocument = errors.New("extractor: doc: encrypted document is not supported")

// ErrUnsupportedDocumentVersion indicates a document older than Word 97.
var ErrUnsupportedDocumentVersion = errors.New("extractor: doc: unsupported document version")

var errDOCCorrupt = errors.New("extractor: doc: unsupported or corrupt .doc file")

type docExtractor struct{}

func (e *docExtractor) Type() FileType { return FileTypeDOC }

func (e *docExtractor) Extract(ctx context.Context, r io.Reader) (string, error) {
	data, err := readSource(ctx, r)
	if err != nil {
		return "", fmt.Errorf("extractor: doc: read: %w", err)
	}

	doc, err := mscfb.New(bytes.NewReader(data))
	if err != nil {
		return "", errDOCCorrupt
	}

	wordData, err := readDOCStream(ctx, doc, "WordDocument", len(data))
	if err != nil {
		return "", err
	}
	fib, err := readDOCFIB(wordData)
	if err != nil {
		return "", err
	}
	tableData, err := readDOCStream(ctx, doc, fib.table, len(data))
	if err != nil {
		return "", err
	}
	clx, ok := docRange(tableData, uint64(fib.fcClx), uint64(fib.lcbClx))
	if !ok {
		return "", errDOCCorrupt
	}
	plc, err := readDOCPieceTable(clx)
	if err != nil {
		return "", err
	}
	text, err := decodeDOCPieces(ctx, wordData, plc, fib.ccpText)
	if err != nil {
		return "", err
	}
	return cleanDOCText(text)
}

func readDOCStream(ctx context.Context, doc *mscfb.Reader, name string, sourceSize int) ([]byte, error) {
	for _, entry := range doc.File {
		if entry.Name != name {
			continue
		}
		if entry.Size <= 0 || entry.Size > int64(sourceSize) {
			return nil, fmt.Errorf("extractor: doc: %s stream size exceeds the file size", name)
		}
		data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: entry}, entry.Size))
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil || int64(len(data)) != entry.Size {
			return nil, errDOCCorrupt
		}
		return data, nil
	}
	return nil, errDOCCorrupt
}

type docFIB struct {
	table   string
	ccpText uint32
	fcClx   uint32
	lcbClx  uint32
}

func readDOCFIB(word []byte) (docFIB, error) {
	var fib docFIB
	if len(word) < 0x22 || binary.LittleEndian.Uint16(word) != 0xA5EC {
		return fib, errDOCCorrupt
	}
	if binary.LittleEndian.Uint16(word[2:]) < 0x00C1 {
		return fib, ErrUnsupportedDocumentVersion
	}
	flags := binary.LittleEndian.Uint16(word[0x0A:])
	if flags&0x0100 != 0 {
		return fib, ErrEncryptedDocument
	}
	fib.table = "0Table"
	if flags&0x0200 != 0 {
		fib.table = "1Table"
	}
	csw := uint64(binary.LittleEndian.Uint16(word[0x20:]))
	cslwOffset := 0x22 + 2*csw
	cslwData, ok := docRange(word, cslwOffset, 2)
	if !ok {
		return fib, errDOCCorrupt
	}
	cslw := uint64(binary.LittleEndian.Uint16(cslwData))
	longs, ok := docRange(word, cslwOffset+2, 4*cslw)
	if !ok || cslw < 4 {
		return fib, errDOCCorrupt
	}
	fib.ccpText = binary.LittleEndian.Uint32(longs[12:])
	if fib.ccpText > 0x7FFFFFFF {
		return fib, errDOCCorrupt
	}
	countOffset := cslwOffset + 2 + 4*cslw
	countData, ok := docRange(word, countOffset, 2)
	if !ok {
		return fib, errDOCCorrupt
	}
	count := uint64(binary.LittleEndian.Uint16(countData))
	blob, ok := docRange(word, countOffset+2, 8*count)
	if !ok || count <= 33 {
		return fib, errDOCCorrupt
	}
	fib.fcClx = binary.LittleEndian.Uint32(blob[33*8:])
	fib.lcbClx = binary.LittleEndian.Uint32(blob[33*8+4:])
	return fib, nil
}

func docRange(data []byte, offset, size uint64) ([]byte, bool) {
	if offset > uint64(len(data)) || size > uint64(len(data))-offset {
		return nil, false
	}
	return data[offset : offset+size], true
}

func readDOCPieceTable(clx []byte) ([]byte, error) {
	for len(clx) > 0 {
		switch clx[0] {
		case 1:
			if len(clx) < 3 {
				return nil, errDOCCorrupt
			}
			size := uint64(binary.LittleEndian.Uint16(clx[1:]))
			if size > 0x7FFF || size > uint64(len(clx))-3 {
				return nil, errDOCCorrupt
			}
			clx = clx[3+size:]
		case 2:
			if len(clx) < 5 {
				return nil, errDOCCorrupt
			}
			size := binary.LittleEndian.Uint32(clx[1:])
			if uint64(size) != uint64(len(clx))-5 {
				return nil, errDOCCorrupt
			}
			return clx[5:], nil
		default:
			return nil, errDOCCorrupt
		}
	}
	return nil, errDOCCorrupt
}

func decodeDOCPieces(ctx context.Context, word, plc []byte, ccpText uint32) (string, error) {
	if len(plc) < 16 || (len(plc)-4)%12 != 0 {
		return "", errDOCCorrupt
	}
	n := (len(plc) - 4) / 12
	if n > 1_000_000 || binary.LittleEndian.Uint32(plc) != 0 {
		return "", errDOCCorrupt
	}
	var previous uint32
	for i := 0; i <= n; i++ {
		cp := binary.LittleEndian.Uint32(plc[4*i:])
		if cp > 0x7FFFFFFF || cp < previous {
			return "", errDOCCorrupt
		}
		previous = cp
	}
	if previous < ccpText {
		return "", errDOCCorrupt
	}
	var text strings.Builder
	for i := range n {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		start := binary.LittleEndian.Uint32(plc[4*i:])
		end := binary.LittleEndian.Uint32(plc[4*(i+1):])
		fc := binary.LittleEndian.Uint32(plc[4*(n+1)+8*i+2:])
		compressed := fc&0x40000000 != 0
		offset := uint64(fc & 0x3FFFFFFF)
		width := uint64(2)
		if compressed {
			offset /= 2
			width = 1
		}
		piece, ok := docRange(word, offset, width*uint64(end-start))
		if !ok {
			return "", errDOCCorrupt
		}
		if start >= ccpText {
			continue
		}
		length := min(end, ccpText) - start
		// Each character position produces at least one decoded byte.
		if err := checkTextLimit(text.Len() + int(length)); err != nil {
			return "", err
		}
		piece = piece[:width*uint64(length)]
		decoder := charmap.Windows1252.NewDecoder()
		if !compressed {
			decoder = unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder()
		}
		decoded, err := decoder.Bytes(piece)
		if err != nil {
			return "", errDOCCorrupt
		}
		if err := checkTextLimit(text.Len() + len(decoded)); err != nil {
			return "", err
		}
		text.Write(decoded)
	}
	return text.String(), nil
}

func cleanDOCText(text string) (string, error) {
	var result strings.Builder
	var fields []bool
	hidden := 0
	for _, character := range text {
		switch character {
		case 0x13:
			fields = append(fields, false)
			hidden++
			continue
		case 0x14:
			if len(fields) > 0 && !fields[len(fields)-1] {
				fields[len(fields)-1] = true
				hidden--
			}
			continue
		case 0x15:
			if len(fields) > 0 {
				if !fields[len(fields)-1] {
					hidden--
				}
				fields = fields[:len(fields)-1]
			}
			continue
		}
		if hidden > 0 {
			continue
		}
		switch character {
		case 0x0D, 0x0B:
			result.WriteByte('\n')
		case 0x0C:
			result.WriteString("\n\n")
		case 0x07:
			result.WriteByte('\t')
		case 0x1E:
			result.WriteByte('-')
		case 0x1F, 0x01, 0x08:
			continue
		default:
			if character < 0x20 {
				character = ' '
			}
			result.WriteRune(character)
		}
		if err := checkTextLimit(result.Len()); err != nil {
			return "", err
		}
	}
	cleaned := strings.TrimSpace(result.String())
	if cleaned == "" {
		return "", errors.New("extractor: doc: no readable text found")
	}
	return cleaned, nil
}

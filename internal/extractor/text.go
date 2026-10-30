package extractor

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
)

type textExtractor struct{}

func (e *textExtractor) Type() FileType { return FileTypeText }

func (e *textExtractor) Extract(ctx context.Context, r io.Reader) (string, error) {
	data, err := readSource(ctx, r)
	if err != nil {
		return "", fmt.Errorf("extractor: text: %w", err)
	}
	var text string
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		var order binary.ByteOrder = binary.LittleEndian
		if data[0] == 0xfe {
			order = binary.BigEndian
		}
		data = data[2:]
		units := make([]uint16, len(data)/2)
		for i := range units {
			units[i] = order.Uint16(data[2*i:])
		}
		text = string(utf16.Decode(units))
		if len(data)%2 != 0 {
			text += "�"
		}
	} else {
		text = string(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	}
	text = strings.TrimSpace(strings.ReplaceAll(strings.ToValidUTF8(text, "�"), "\x00", ""))
	if err := checkTextLimit(len(text)); err != nil {
		return "", err
	}
	if len(text) < 10 {
		return "", fmt.Errorf("extractor: text: %w", ErrNoText)
	}
	return text, nil
}

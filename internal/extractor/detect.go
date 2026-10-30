package extractor

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Content verification and extraction failures have stable identities.
var (
	ErrTypeMismatch    = errors.New("extractor: file extension does not match content")
	ErrUnsupportedType = errors.New("extractor: unsupported file type")
	ErrNoText          = errors.New("extractor: no readable text found")
)

// Sniff identifies a supported format from its content, or returns an empty type.
func Sniff(data []byte) FileType {
	return sniff(context.Background(), data)
}

func sniff(ctx context.Context, data []byte) FileType {
	if bytes.Contains(data[:min(len(data), 1024)], []byte("%PDF-")) {
		return FileTypePDF
	}
	if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err == nil {
			if docxPart(zr, "word/document.xml") != nil {
				return FileTypeDOCX
			}
			main, err := docxRelatedMainPart(ctx, zr)
			if err == nil && main != "" {
				return FileTypeDOCX
			}
		}
		return ""
	}
	if bytes.HasPrefix(data, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
		if sniffWordDocument(data) {
			return FileTypeDOC
		}
		return ""
	}
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		return FileTypeText
	}
	sample := data[:min(len(data), 8<<10)]
	if len(data) > len(sample) {
		end := len(sample)
		for len(sample) > 0 && !utf8.RuneStart(data[len(sample)]) {
			sample = sample[:len(sample)-1]
		}
		if len(sample) < end {
			if _, size := utf8.DecodeRune(data[len(sample):]); size == 1 {
				return ""
			}
		}
	}
	if len(sample) > 0 && !bytes.ContainsRune(sample, 0) && utf8.Valid(sample) {
		return FileTypeText
	}
	return ""
}

// Read only allocation tables and directory records, without traversing storage trees.
func sniffWordDocument(data []byte) bool {
	if len(data) < 512 {
		return false
	}
	shift := binary.LittleEndian.Uint16(data[30:])
	if shift != 9 && shift != 12 {
		return false
	}
	sectorWidth := uint32(1) << shift
	sectorSize := int(sectorWidth)
	sectorCount := len(data)/sectorSize - 1
	sector := func(id uint32) []byte {
		if uint64(id) >= uint64(max(sectorCount, 0)) {
			return nil
		}
		start := (int(id) + 1) * sectorSize
		return data[start : start+sectorSize]
	}
	fatCount := binary.LittleEndian.Uint32(data[44:])
	if fatCount == 0 || uint64(fatCount) > uint64(max(sectorCount, 0)) {
		return false
	}
	fatSectors := make([]uint32, 0)
	addFAT := func(table []byte) bool {
		for i := 0; i < len(table) && uint64(len(fatSectors)) < uint64(fatCount); i += 4 {
			id := binary.LittleEndian.Uint32(table[i:])
			if id == 0xffffffff {
				continue
			}
			if sector(id) == nil {
				return false
			}
			fatSectors = append(fatSectors, id)
		}
		return true
	}
	if !addFAT(data[76:512]) {
		return false
	}
	difat := binary.LittleEndian.Uint32(data[68:])
	difatCount := binary.LittleEndian.Uint32(data[72:])
	if uint64(difatCount) > uint64(max(sectorCount, 0)) {
		return false
	}
	seen := make(map[uint32]bool)
	for i := uint32(0); uint64(len(fatSectors)) < uint64(fatCount) && i < difatCount; i++ {
		part := sector(difat)
		if part == nil || seen[difat] {
			return false
		}
		seen[difat] = true
		if !addFAT(part[:sectorSize-4]) {
			return false
		}
		difat = binary.LittleEndian.Uint32(part[sectorSize-4:])
	}
	if uint64(len(fatSectors)) != uint64(fatCount) {
		return false
	}
	directory := binary.LittleEndian.Uint32(data[48:])
	clear(seen)
	for directory != 0xfffffffe {
		part := sector(directory)
		if part == nil || seen[directory] {
			return false
		}
		seen[directory] = true
		for offset := 0; offset < sectorSize; offset += 128 {
			entry := part[offset : offset+128]
			if entry[66] == 2 && binary.LittleEndian.Uint16(entry[64:]) == 26 && bytes.Equal(entry[:26], []byte("W\x00o\x00r\x00d\x00D\x00o\x00c\x00u\x00m\x00e\x00n\x00t\x00\x00\x00")) {
				return true
			}
		}
		fatIndex := uint64(directory) / uint64(sectorWidth/4)
		if fatIndex >= uint64(len(fatSectors)) {
			return false
		}
		fat := sector(fatSectors[fatIndex])
		directory = binary.LittleEndian.Uint32(fat[int(directory)%(sectorSize/4)*4:])
	}
	return false
}

// Resolve reconciles a filename extension with the detected content format.
func Resolve(filename string, data []byte) (FileType, error) {
	return ResolveContext(context.Background(), filename, data)
}

// ResolveContext verifies content while preserving cancellation during package reads.
func ResolveContext(ctx context.Context, filename string, data []byte) (FileType, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	sniffed := sniff(ctx, data)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if sniffed == "" {
		return "", ErrUnsupportedType
	}
	var extension FileType
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".pdf":
		extension = FileTypePDF
	case ".docx":
		extension = FileTypeDOCX
	case ".doc":
		extension = FileTypeDOC
	case ".txt", ".md", ".markdown":
		extension = FileTypeText
	}
	if extension != "" && extension != sniffed {
		return "", fmt.Errorf("%w: %s", ErrTypeMismatch, filename)
	}
	return sniffed, nil
}

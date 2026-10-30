package extractor

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

type cfbStream struct {
	name string
	data []byte
}

type cfbOptions struct {
	declaredSize *uint32
	cyclic       bool
}

func buildCFB(t *testing.T, streams []cfbStream, opts cfbOptions) []byte {
	t.Helper()
	const (
		sectorSize = 512
		free       = uint32(0xFFFFFFFF)
		end        = uint32(0xFFFFFFFE)
		fat        = uint32(0xFFFFFFFD)
	)
	if len(streams) == 0 || len(streams) > 3 {
		t.Fatal("a directory sector requires one to three streams")
	}
	sectors := make([]int, len(streams))
	dataSectors := 0
	for i, stream := range streams {
		sectors[i] = max(8, (len(stream.data)+sectorSize-1)/sectorSize)
		dataSectors += sectors[i]
	}
	fatSectors := 1
	for fatSectors*128 < 1+dataSectors+fatSectors {
		fatSectors++
	}
	if fatSectors > 109 {
		t.Fatal("streams exceed the header FAT capacity")
	}
	data := make([]byte, (2+fatSectors+dataSectors)*sectorSize)
	binary.LittleEndian.PutUint64(data, 0xE11AB1A1E011CFD0)
	binary.LittleEndian.PutUint16(data[24:], 0x003E)
	binary.LittleEndian.PutUint16(data[26:], 3)
	binary.LittleEndian.PutUint16(data[28:], 0xFFFE)
	binary.LittleEndian.PutUint16(data[30:], 9)
	binary.LittleEndian.PutUint16(data[32:], 6)
	binary.LittleEndian.PutUint32(data[44:], uint32(fatSectors))
	binary.LittleEndian.PutUint32(data[48:], uint32(fatSectors))
	binary.LittleEndian.PutUint32(data[56:], 4096)
	binary.LittleEndian.PutUint32(data[60:], end)
	binary.LittleEndian.PutUint32(data[68:], end)
	for i := range 109 {
		value := free
		if i < fatSectors {
			value = uint32(i)
		}
		binary.LittleEndian.PutUint32(data[76+4*i:], value)
	}
	fatData := data[sectorSize : (1+fatSectors)*sectorSize]
	for i := 0; i < len(fatData); i += 4 {
		binary.LittleEndian.PutUint32(fatData[i:], free)
	}
	setFAT := func(sector int, next uint32) {
		binary.LittleEndian.PutUint32(fatData[4*sector:], next)
	}
	for i := range fatSectors {
		setFAT(i, fat)
	}
	setFAT(fatSectors, end)
	directory := data[(1+fatSectors)*sectorSize : (2+fatSectors)*sectorSize]
	writeEntry := func(index int, name string, kind byte, start, size uint32) {
		entry := directory[index*128 : (index+1)*128]
		encoded := utf16.Encode([]rune(name))
		nameUnits := len(encoded)
		if nameUnits > 31 {
			t.Fatal("stream name exceeds the directory entry")
			return
		}
		for i, character := range encoded {
			binary.LittleEndian.PutUint16(entry[2*i:], character)
		}
		nameLength := uint16(nameUnits)
		binary.LittleEndian.PutUint16(entry[64:], 2*(nameLength+1))
		entry[66], entry[67] = kind, 1
		for _, offset := range []int{68, 72, 76} {
			binary.LittleEndian.PutUint32(entry[offset:], free)
		}
		binary.LittleEndian.PutUint32(entry[116:], start)
		binary.LittleEndian.PutUint32(entry[120:], size)
	}
	writeEntry(0, "Root Entry", 5, end, 0)
	binary.LittleEndian.PutUint32(directory[76:], 1)
	start := fatSectors + 1
	for i, stream := range streams {
		streamSize := len(stream.data)
		if streamSize > 7<<20 {
			t.Fatal("stream exceeds the header FAT capacity")
			return nil
		}
		size := max(uint32(4096), uint32(streamSize))
		if opts.declaredSize != nil {
			size = *opts.declaredSize
		}
		writeEntry(i+1, stream.name, 2, uint32(start), size)
		if i+1 < len(streams) {
			binary.LittleEndian.PutUint32(directory[(i+1)*128+72:], uint32(i+2))
		}
		copy(data[(start+1)*sectorSize:], stream.data)
		for j := range sectors[i] {
			next := uint32(start + j + 1)
			if j+1 == sectors[i] {
				next = end
			}
			if opts.cyclic {
				next = uint32(start + j)
			}
			setFAT(start+j, next)
		}
		start += sectors[i]
	}
	return data
}

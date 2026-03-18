// Package extractor provides text extraction from various file formats.
package extractor

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// FileType identifies a supported file format.
type FileType string

const (
	FileTypeText FileType = "text"
	FileTypePDF  FileType = "pdf"
	FileTypeDOCX FileType = "docx"
	FileTypeDOC  FileType = "doc"
)

// Extractor reads a file and returns its text content.
type Extractor interface {
	Extract(ctx context.Context, r io.Reader) (string, error)
	Type() FileType
}

// DetectFileType detects the file type from the filename extension.
// Returns an error if the extension is not supported.
func DetectFileType(filename string) (FileType, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return FileTypePDF, nil
	case ".docx":
		return FileTypeDOCX, nil
	case ".doc":
		return FileTypeDOC, nil
	default:
		return "", fmt.Errorf("extractor: unsupported file extension %q", ext)
	}
}

// New returns the correct Extractor for the given FileType.
func New(ft FileType) Extractor {
	switch ft {
	case FileTypePDF:
		return &pdfExtractor{}
	case FileTypeDOCX:
		return &docxExtractor{}
	case FileTypeDOC:
		return &docExtractor{}
	case FileTypeText:
		return &textExtractor{}
	default:
		return &textExtractor{}
	}
}

// ContentTypeFromFileType returns the MIME content type for a FileType.
func ContentTypeFromFileType(ft FileType) string {
	switch ft {
	case FileTypePDF:
		return "application/pdf"
	case FileTypeDOCX:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case FileTypeDOC:
		return "application/msword"
	default:
		return "application/octet-stream"
	}
}

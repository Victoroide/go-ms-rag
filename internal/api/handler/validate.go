package handler

import (
	"encoding/json"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

func validateDocType(value string) (string, *APIError) {
	value = norm.NFC.String(strings.TrimSpace(value))
	if len(value) > 128 {
		return "", &APIError{Error: "validation", Message: "document type exceeds maximum length of 128 bytes"}
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", &APIError{Error: "validation", Message: "document type must not contain control or format characters"}
		}
	}
	return value, nil
}

func validateMetadata(metadata map[string]any) *APIError {
	data, err := json.Marshal(metadata)
	if err != nil {
		return &APIError{Error: "validation", Message: "metadata must be a JSON object"}
	}
	if len(data) > 65536 {
		return &APIError{Error: "validation", Message: "metadata exceeds maximum size of 65536 bytes"}
	}
	if !metadataWithinDepth(metadata, 1) {
		return &APIError{Error: "validation", Message: "metadata exceeds maximum nesting depth of 8"}
	}
	if metadataHasNUL(metadata) {
		return &APIError{Error: "validation", Message: "metadata must not contain NUL characters"}
	}
	return nil
}

func metadataWithinDepth(value any, depth int) bool {
	switch v := value.(type) {
	case map[string]any:
		if depth > 8 {
			return false
		}
		for _, item := range v {
			if !metadataWithinDepth(item, depth+1) {
				return false
			}
		}
	case []any:
		if depth > 8 {
			return false
		}
		for _, item := range v {
			if !metadataWithinDepth(item, depth+1) {
				return false
			}
		}
	}
	return true
}

func metadataHasNUL(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.ContainsRune(v, 0)
	case map[string]any:
		for key, item := range v {
			if strings.ContainsRune(key, 0) || metadataHasNUL(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if metadataHasNUL(item) {
				return true
			}
		}
	}
	return false
}

func effectiveMaxChunks(requested, configured int) int {
	if requested == 0 {
		return configured
	}
	return min(requested, configured)
}

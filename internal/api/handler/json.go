package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"
)

func isJSONRequest(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err == nil {
		err = decodeJSONObject(body, dst)
		if err == nil {
			return true
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "validation", "request body exceeds maximum size")
	} else {
		message := "invalid JSON body"
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			message = strings.TrimPrefix(err.Error(), "json: ")
		} else if strings.HasPrefix(err.Error(), "duplicate field ") {
			message = err.Error()
		}
		writeJSONError(w, http.StatusBadRequest, "validation", message)
	}
	return false
}

func decodeJSONObject(body []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return errors.New("JSON body must be an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		key, tokenErr := decoder.Token()
		if tokenErr != nil {
			return tokenErr
		}
		field, ok := key.(string)
		if !ok {
			return errors.New("invalid JSON object key")
		}
		foldedField := foldJSONField(field)
		if _, exists := seen[foldedField]; exists {
			return fmt.Errorf("duplicate field %q", field)
		}
		seen[foldedField] = struct{}{}
		depth := 0
		for {
			value, valueErr := decoder.Token()
			if valueErr != nil {
				return valueErr
			}
			if delim, isDelim := value.(json.Delim); isDelim {
				switch delim {
				case '{', '[':
					depth++
				case '}', ']':
					depth--
				}
			}
			if depth == 0 {
				break
			}
		}
	}
	if _, err = decoder.Token(); err != nil {
		return err
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	decoder = json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}

func foldJSONField(field string) string {
	return strings.Map(func(r rune) rune {
		for {
			next := unicode.SimpleFold(r)
			if next <= r {
				return next
			}
			r = next
		}
	}, field)
}

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Victoroide/quarry-rag/internal/extractor"
)

func writeExtractionError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "extract uploaded text", "error", err)
	status, code, message := http.StatusUnprocessableEntity, "unparseable", "document could not be parsed"
	switch {
	case extractionErrorIs(err, extractor.ErrTypeMismatch):
		status, code, message = http.StatusUnsupportedMediaType, "type_mismatch", "file extension does not match its content"
	case extractionErrorIs(err, extractor.ErrUnsupportedType):
		status, code, message = http.StatusUnsupportedMediaType, "unsupported_type", "unsupported file type; accepted formats: pdf, docx, doc, txt, md, markdown"
	case extractionErrorIs(err, extractor.ErrContentTooLarge):
		code, message = "too_large", "extracted text exceeds the size limit"
	case extractionErrorIs(err, extractor.ErrExtractionTimeout), extractionErrorIs(err, context.DeadlineExceeded):
		code, message = "timeout", "document extraction exceeded the time limit"
	case extractionErrorIs(err, extractor.ErrNoText):
		code, message = "no_text", "document contains no readable text"
	case extractionErrorIs(err, extractor.ErrEncryptedDocument):
		code, message = "encrypted", "encrypted documents are not supported"
	case extractionErrorIs(err, extractor.ErrUnsupportedDocumentVersion):
		code, message = "unsupported_version", "document version is not supported"
	case extractionErrorIs(err, extractor.ErrExtractionUnavailable):
		status, code, message = http.StatusServiceUnavailable, "unavailable", "document extraction is unavailable"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Code    string `json:"code"`
	}{"extraction", message, code}); err != nil {
		slog.ErrorContext(r.Context(), "encode extraction error", "error", err)
	}
}

func extractionErrorIs(err, target error) bool {
	// The child protocol preserves error messages but not wrapped error identities.
	return errors.Is(err, target) || err.Error() == target.Error() || strings.HasSuffix(err.Error(), ": "+target.Error())
}

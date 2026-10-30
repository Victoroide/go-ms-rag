package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/extractor"
)

type uploadObservedReader struct {
	*bytes.Reader
	read func()
}

func (r uploadObservedReader) Read(p []byte) (int, error) {
	r.read()
	return r.Reader.Read(p)
}

func uploadRequest(t *testing.T) (*http.Request, *bytes.Buffer) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "archive.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("%PDF-1.4\ndocument bytes")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req, &body
}

func TestUploadSlotIsHeldDuringRead(t *testing.T) {
	limiter := extractor.NewLimiter(1, 0)
	release, err := limiter.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	req, body := uploadRequest(t)
	read := false
	req.Body = uploadReadCloser{uploadObservedReader{bytes.NewReader(body.Bytes()), func() { read = true }}}
	rec := httptest.NewRecorder()
	IngestHandler(nil, nil, nil, 20<<20, WithUploadLimiter(limiter))(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" || read {
		t.Fatalf("busy upload: status=%d retry=%q read=%v", rec.Code, rec.Header().Get("Retry-After"), read)
	}
	var response APIError
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &response); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if response.Error != "busy" || response.Message != "too many concurrent uploads" {
		t.Fatalf("busy response: %+v", response)
	}
	release()
	req, body = uploadRequest(t)
	req.Body = uploadReadCloser{uploadObservedReader{bytes.NewReader(body.Bytes()), func() {
		if free, acquireErr := limiter.Acquire(t.Context()); !errors.Is(acquireErr, extractor.ErrBusy) {
			if free != nil {
				free()
			}
			t.Errorf("upload read without holding a slot: %v", acquireErr)
		}
	}}}
	rec = httptest.NewRecorder()
	IngestHandler(nil, nil, nil, 20<<20, WithUploadLimiter(limiter), WithExtractor(func(context.Context, extractor.FileType, []byte) (string, error) {
		return "", extractor.ErrExtractionFailed
	}))(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("admitted upload: %d %s", rec.Code, rec.Body)
	}
	free, err := limiter.Acquire(t.Context())
	if err != nil {
		t.Fatalf("slot not released: %v", err)
	}
	free()
}

type uploadReadCloser struct{ uploadObservedReader }

func (uploadReadCloser) Close() error { return nil }

func TestUploadExtractionErrors(t *testing.T) {
	for _, tt := range []struct {
		err     error
		status  int
		message string
	}{
		{extractor.ErrExtractionTimeout, 422, "document extraction exceeded the time limit"},
		{extractor.ErrExtractionFailed, 422, "document could not be parsed"},
		{extractor.ErrExtractionUnavailable, 503, "document extraction is unavailable"},
		{errors.New("invalid document"), 422, "document could not be parsed"},
	} {
		t.Run(tt.message, func(t *testing.T) {
			req, _ := uploadRequest(t)
			rec := httptest.NewRecorder()
			IngestHandler(nil, nil, nil, 20<<20, WithExtractor(func(ctx context.Context, ft extractor.FileType, data []byte) (string, error) {
				if ctx != req.Context() || ft != extractor.FileTypePDF || string(data) != "%PDF-1.4\ndocument bytes" {
					t.Errorf("unexpected extraction input: type=%q data=%q", ft, data)
				}
				return "", tt.err
			}))(rec, req)
			var response APIError
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tt.status || response.Error != "extraction" || response.Message != tt.message {
				t.Fatalf("extraction failure: status=%d response=%+v", rec.Code, response)
			}
		})
	}
}

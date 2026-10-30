package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/logctx"
	"github.com/google/uuid"
)

func TestRecoveryReturnsJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	Recovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("test") })).ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody))
	if rec.Code != 500 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("invalid recovery response: %d %s", rec.Code, rec.Header())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "internal" {
		t.Fatalf("invalid error body: %s", rec.Body)
	}
}

func captureMiddlewareLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(logctx.NewHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func TestRequestIDPropagates(t *testing.T) {
	for _, id := range []string{"trace.1_A:b-c", strings.Repeat("x", 128), "", "with space", "bad\nheader", "bad\ttab", "é", "slash/id", strings.Repeat("x", 129)} {
		t.Run(id, func(t *testing.T) {
			output := captureMiddlewareLogs(t)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
			req.Header.Set("X-Request-ID", id)
			rec := httptest.NewRecorder()
			var contextID string
			RequestID(Logging(Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contextID = logctx.RequestID(r.Context())
				w.WriteHeader(http.StatusNoContent)
			})))).ServeHTTP(rec, req)
			got := rec.Header().Get("X-Request-ID")
			if got == "" || got != contextID {
				t.Fatalf("response ID %q differs from context ID %q", got, contextID)
			}
			if id == "trace.1_A:b-c" || len(id) == 128 {
				if got != id {
					t.Fatalf("valid ID replaced: %q", got)
				}
			} else if parsed, err := uuid.Parse(got); err != nil || parsed.Version() != 7 {
				t.Fatalf("expected generated UUIDv7, got %q: %v", got, err)
			}
			var entry map[string]any
			if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			if entry["request_id"] != got {
				t.Fatalf("access log has wrong ID: %v", entry)
			}
		})
	}
}

func TestRequestIDsAreUnique(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	seen := make(map[string]bool)
	for range 10 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
		id := rec.Header().Get("X-Request-ID")
		if id == "" || seen[id] {
			t.Fatalf("empty or repeated request ID: %q", id)
		}
		seen[id] = true
	}
}

func TestPanicIsLoggedWith500(t *testing.T) {
	output := captureMiddlewareLogs(t)
	rec := httptest.NewRecorder()
	Logging(Recovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("test panic")
	}))).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/panic", http.NoBody))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("response status = %d", rec.Code)
	}
	var access, panicLog map[string]any
	decoder := json.NewDecoder(output)
	if err := decoder.Decode(&panicLog); err != nil {
		t.Fatal(err)
	}
	if panicLog["level"] != "ERROR" || !strings.Contains(panicLog["stack"].(string), "TestPanicIsLoggedWith500") {
		t.Fatalf("invalid panic log: %v", panicLog)
	}
	if err := decoder.Decode(&access); err != nil {
		t.Fatal(err)
	}
	if access["status"] != float64(500) || access["bytes"] != float64(rec.Body.Len()) {
		t.Fatalf("invalid access log: %v", access)
	}
}

func TestAbortHandlerIsRepanicked(t *testing.T) {
	output := captureMiddlewareLogs(t)
	rec := httptest.NewRecorder()
	defer func() {
		if got, ok := recover().(error); !ok || !errors.Is(got, http.ErrAbortHandler) {
			t.Errorf("panic = %v, want http.ErrAbortHandler", got)
		}
		if output.Len() != 0 || rec.Body.Len() != 0 {
			t.Errorf("abort wrote response or logs: %q %q", rec.Body.String(), output.String())
		}
	}()
	Logging(Recovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/abort", http.NoBody))
}

func TestRecoveryDoesNotWriteAfterPartialResponse(t *testing.T) {
	for _, withLogging := range []bool{false, true} {
		for _, headerOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("logging=%t/header=%t", withLogging, headerOnly), func(t *testing.T) {
				captureMiddlewareLogs(t)
				h := Recovery(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/plain")
					if headerOnly {
						w.WriteHeader(http.StatusAccepted)
					} else if _, err := w.Write([]byte("partial")); err != nil {
						t.Fatal(err)
					}
					panic("after write")
				}))
				if withLogging {
					h = Logging(h)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/partial", http.NoBody))
				wantStatus, wantBody := http.StatusOK, "partial"
				if headerOnly {
					wantStatus, wantBody = http.StatusAccepted, ""
				}
				if rec.Code != wantStatus || rec.Body.String() != wantBody || rec.Header().Get("Content-Type") != "text/plain" {
					t.Fatalf("response changed after panic: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
				}
			})
		}
	}
}

func TestAccessLogFields(t *testing.T) {
	output := captureMiddlewareLogs(t)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/documents?secret=hidden", http.NoBody)
	req.RemoteAddr = "192.0.2.1:4567"
	rec := httptest.NewRecorder()
	Logging(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		if _, err := w.Write([]byte("saved")); err != nil {
			t.Fatal(err)
		}
	})).ServeHTTP(rec, req)
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["method"] != "POST" || entry["path"] != "/documents" || entry["status"] != float64(201) || entry["bytes"] != float64(5) || entry["remote_addr"] != "192.0.2.1:4567" {
		t.Fatalf("invalid access log: %v", entry)
	}
	if duration, ok := entry["duration_ms"].(float64); !ok || duration < 0 {
		t.Fatalf("invalid duration: %v", entry)
	}
	if strings.Contains(output.String(), "hidden") {
		t.Fatal("query string was logged")
	}
}
func TestResponseWriterKeepsFirstStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec, statusCode: 200}
	rw.WriteHeader(201)
	rw.WriteHeader(500)
	if rw.statusCode != 201 {
		t.Fatalf("logged status %d differs from sent status %d", rw.statusCode, rec.Code)
	}
}

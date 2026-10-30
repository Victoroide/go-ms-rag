package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEitherRotationTokenIsAccepted(t *testing.T) {
	tokens := []string{"old-secret", "new-secret-with-a-different-length"}
	h := RequireServiceTokens(tokens, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, tc := range []struct {
		token string
		want  int
	}{
		{"old-secret", http.StatusNoContent},
		{"new-secret-with-a-different-length", http.StatusNoContent},
		{"old-secre", http.StatusUnauthorized},
		{"old-secret-extra", http.StatusUnauthorized},
		{"wrong-secret", http.StatusUnauthorized},
	} {
		t.Run(tc.token, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/documents", http.NoBody)
			r.Header.Set("X-Service-Token", tc.token)
			w := httptest.NewRecorder()
			h(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestMissingAndEmptyHeaderAreRejected(t *testing.T) {
	h := RequireServiceTokens([]string{"secret"}, func(http.ResponseWriter, *http.Request) { t.Error("unauthorized request reached handler") })
	for _, header := range []http.Header{nil, {"X-Service-Token": {""}}} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/documents", http.NoBody)
		r.Header = header
		w := httptest.NewRecorder()
		h(w, r)
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusUnauthorized || w.Header().Get("Content-Type") != "application/json" || body["error"] != "unauthorized" {
			t.Fatalf("unexpected unauthorized response: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestEmptyTokenListLeavesRoutesOpen(t *testing.T) {
	for _, tokens := range [][]string{nil, {}} {
		h := RequireServiceTokens(tokens, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		for _, token := range []string{"", "arbitrary"} {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/documents", http.NoBody)
			r.Header.Set("X-Service-Token", token)
			w := httptest.NewRecorder()
			h(w, r)
			if w.Code != http.StatusNoContent {
				t.Fatalf("empty token list rejected request: %d", w.Code)
			}
		}
	}
}

func TestServiceTokenDigestsAreCapturedAtSetup(t *testing.T) {
	tokens := []string{"original-secret"}
	h := RequireServiceTokens(tokens, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	tokens[0] = "replacement-secret"
	for _, tc := range []struct {
		token string
		want  int
	}{{"original-secret", http.StatusNoContent}, {"replacement-secret", http.StatusUnauthorized}} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/documents", http.NoBody)
		r.Header.Set("X-Service-Token", tc.token)
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d, want %d", w.Code, tc.want)
		}
	}
}

func TestServiceAuthStateLogsOnlyTokenCount(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, tokens := range [][]string{nil, {"private-first", "private-second"}} {
		logs.Reset()
		LogServiceAuthState(tokens)
		var record map[string]any
		if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record["count"] != float64(len(tokens)) || len(record) != 4 || strings.Contains(logs.String(), "private") {
			t.Fatalf("expected only token count: %s", logs.Bytes())
		}
	}
}

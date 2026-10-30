package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/embedder"
	"github.com/Victoroide/quarry-rag/internal/extractor"
	"github.com/Victoroide/quarry-rag/internal/llm"
	"github.com/Victoroide/quarry-rag/internal/retriever"
	"github.com/Victoroide/quarry-rag/internal/store/memory"
	"github.com/stretchr/testify/require"
)

type validationAcceptStore struct {
	domain.ChunkStore
	accepted []domain.AcceptRequest
}

func (s *validationAcceptStore) AcceptDocument(_ context.Context, req domain.AcceptRequest) (domain.AcceptResult, error) {
	s.accepted = append(s.accepted, req)
	return domain.AcceptResult{DocumentID: req.Document.ID, Outcome: domain.AcceptAccepted}, nil
}

func validationJSONRequest(t *testing.T, path, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func validationQueryHandler(maxChunks int) http.HandlerFunc {
	cfg := &config.Config{MaxChunksPerQuery: maxChunks, BedrockFallbackModel: "fallback"}
	amb := llm.NewAmbassador(cfg)
	amb.SetClient(llm.NewFakeConverser())
	return QueryHandler(retriever.NewRetriever(memory.NewFakeVectorStore(), embedder.NewFakeEmbedder(256), 0.7, 10), amb, cfg, nil)
}

func TestQueryRejectsUnknownField(t *testing.T) {
	for _, field := range []string{"allowed_document_id", "allowedDocumentIds"} {
		t.Run(field, func(t *testing.T) {
			rec := httptest.NewRecorder()
			validationQueryHandler(10)(rec, validationJSONRequest(t, "/query", `{"query":"archive","`+field+`":[]}`))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			var apiErr APIError
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
			require.Equal(t, `unknown field "`+field+`"`, apiErr.Message)
		})
	}
}

func TestQueryRejectsDuplicateKeys(t *testing.T) {
	for _, body := range []string{
		`{"query":"archive","allowed_document_ids":[],"allowed_document_ids":null}`,
		`{"query":"archive","allowed_document_ids":[],"allowed_document_\u0069ds":null}`,
		`{"query":"archive","query":"private"}`,
	} {
		rec := httptest.NewRecorder()
		validationQueryHandler(10)(rec, validationJSONRequest(t, "/query", body))
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		var apiErr APIError
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
		require.Contains(t, apiErr.Message, "duplicate field")
		if strings.Contains(body, "allowed_document") {
			require.Equal(t, `duplicate field "allowed_document_ids"`, apiErr.Message)
		}
	}
}

func TestQueryRejectsFoldedDuplicateKeys(t *testing.T) {
	for _, tc := range []struct {
		name, body, secondKey string
	}{
		{"ascii", `{"query":"archive","allowed_document_ids":["aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"],"ALLOWED_DOCUMENT_IDS":null}`, "ALLOWED_DOCUMENT_IDS"},
		{"long_s", `{"query":"archive","allowed_document_ids":["aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"],"allowed_document_idſ":null}`, "allowed_document_idſ"},
		{"kelvin", `{"query":"archive","max_chunks":2,"max_chunKs":3}`, "max_chunKs"},
		{"kelvin_first", `{"query":"archive","max_chunKs":2,"max_chunks":3}`, "max_chunks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			validationQueryHandler(10)(rec, validationJSONRequest(t, "/query", tc.body))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			var apiErr APIError
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
			require.Equal(t, "validation", apiErr.Error)
			require.Equal(t, `duplicate field "`+tc.secondKey+`"`, apiErr.Message)
		})
	}
}

func TestQueryAcceptsSingleFoldedKey(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		topK       int
	}{
		{"ascii", `{"QUERY":"archive","MODE":"global","MAX_CHUNKS":2}`, 2},
		{"uppercase_allowlist", `{"query":"archive","mode":"global","ALLOWED_DOCUMENT_IDS":[]}`, 0},
		{"long_s", `{"query":"archive","mode":"global","allowed_document_idſ":[]}`, 0},
		{"kelvin", `{"query":"archive","mode":"global","max_chunKs":2}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &validationVectorStore{}
			cfg := &config.Config{MaxChunksPerQuery: 3}
			h := QueryHandler(retriever.NewRetriever(store, embedder.NewFakeEmbedder(256), 0.7, 10), nil, cfg, nil)
			rec := httptest.NewRecorder()
			h(rec, validationJSONRequest(t, "/query", tc.body))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, tc.topK, store.topK)
		})
	}
}

func TestIngestRejectsUnknownField(t *testing.T) {
	for _, body := range []string{
		`{"content":"document text","doc_typ":"report"}`,
		`{"content":"document text","metadata":{"nested":[{"x":1}]},"extra":true}`,
	} {
		store := &validationAcceptStore{}
		rec := httptest.NewRecorder()
		IngestHandler(nil, store, nil, 1<<20)(rec, validationJSONRequest(t, "/ingest", body))
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Empty(t, store.accepted)
	}
}

func TestDocTypeValidation(t *testing.T) {
	for _, tc := range []struct {
		name, label, normalized string
		status                  int
	}{
		{"too_long", strings.Repeat("x", 129), "", 400},
		{"format", "a\u202eb", "", 400},
		{"control", "a\nb", "", 400},
		{"nul", "a\x00b", "", 400},
		{"normalized", "  cafe\u0301  ", "café", 202},
		{"boundary", strings.Repeat("é", 64), strings.Repeat("é", 64), 202},
		{"multibyte_too_long", strings.Repeat("é", 65), "", 400},
		{"empty", "  ", "", 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, upload := range []bool{false, true} {
				store := &validationAcceptStore{}
				var r *http.Request
				if upload {
					r = validationMultipartRequest(t, map[string]string{"doc_type": tc.label})
				} else {
					body, err := json.Marshal(IngestRequest{Content: "document text", DocType: tc.label})
					require.NoError(t, err)
					r = validationJSONRequest(t, "/ingest", string(body))
				}
				rec := httptest.NewRecorder()
				validationIngestHandler(store)(rec, r)
				require.Equal(t, tc.status, rec.Code, rec.Body.String())
				if tc.status == 202 {
					require.Len(t, store.accepted, 1)
					require.Equal(t, tc.normalized, store.accepted[0].DocType)
				} else {
					require.Empty(t, store.accepted)
				}
			}
			body, err := json.Marshal(QueryRequest{Query: "archive", FilterDocType: tc.label, AllowedDocumentIDs: []string{}})
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			validationQueryHandler(10)(rec, validationJSONRequest(t, "/query", string(body)))
			want := tc.status
			if want == 202 {
				want = 200
			}
			require.Equal(t, want, rec.Code, rec.Body.String())
		})
	}
}

func TestMetadataLimits(t *testing.T) {
	depthEight := `{"value":` + strings.Repeat(`[`, 7) + `"x"` + strings.Repeat(`]`, 7) + `}`
	depthNine := `{"value":` + strings.Repeat(`[`, 8) + `"x"` + strings.Repeat(`]`, 8) + `}`
	for _, tc := range []struct {
		name, metadata string
		status         int
	}{
		{"at_byte_limit", `{"v":"` + strings.Repeat("x", 65528) + `"}`, 202},
		{"over_byte_limit", `{"v":"` + strings.Repeat("x", 65529) + `"}`, 400},
		{"escaped_over_limit", `{"v":"` + strings.Repeat(`<`, 10922) + `"}`, 400},
		{"at_depth_limit", depthEight, 202},
		{"over_depth_limit", depthNine, 400},
		{"nul_key", `{"a\u0000":"x"}`, 400},
		{"nul_value", `{"nested":[{"value":"a\u0000b"}]}`, 400},
		{"arbitrary_keys", `{"content":{"query":[1,true,null]},"other":"ok"}`, 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &validationAcceptStore{}
			rec := httptest.NewRecorder()
			validationIngestHandler(store)(rec, validationJSONRequest(t, "/ingest", `{"content":"text","metadata":`+tc.metadata+`}`))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.status == 400 {
				require.Empty(t, store.accepted)
			} else {
				require.Len(t, store.accepted, 1)
			}
		})
	}
}

func validationMultipartRequest(t *testing.T, fields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	file, err := mw.CreateFormFile("file", "report.pdf")
	require.NoError(t, err)
	_, err = file.Write([]byte("uploaded bytes"))
	require.NoError(t, err)
	for key, value := range fields {
		require.NoError(t, mw.WriteField(key, value))
	}
	require.NoError(t, mw.Close())
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

func validationIngestHandler(store *validationAcceptStore) http.HandlerFunc {
	return IngestHandler(nil, store, nil, 1<<20, WithExtractor(func(context.Context, extractor.FileType, []byte) (string, error) {
		return "document text", nil
	}))
}

func TestMultipartMetadataField(t *testing.T) {
	for _, tc := range []struct {
		name, metadata string
		status         int
	}{
		{"object", `{"author":"operator","nested":{"values":[1,true]},"source_file":"forged"}`, 202},
		{"malformed", `{"author":`, 400},
		{"array", `[]`, 400},
		{"null", `null`, 400},
		{"empty", ``, 400},
		{"trailing", `{} {}`, 400},
		{"duplicate", `{"author":"a","author":"b"}`, 400},
		{"nul", `{"nested":["a\u0000b"]}`, 400},
		{"nul_source_file", `{"source_file":"a\u0000b"}`, 400},
		{"over_size", `{"v":"` + strings.Repeat("x", 65529) + `"}`, 400},
		{"over_depth", `{"v":` + strings.Repeat(`[`, 8) + `0` + strings.Repeat(`]`, 8) + `}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &validationAcceptStore{}
			rec := httptest.NewRecorder()
			validationIngestHandler(store)(rec, validationMultipartRequest(t, map[string]string{"metadata": tc.metadata}))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.status == 400 {
				require.Empty(t, store.accepted)
				return
			}
			require.Len(t, store.accepted, 1)
			require.Equal(t, "operator", store.accepted[0].Document.Metadata["author"])
			require.Equal(t, "report.pdf", store.accepted[0].Document.Metadata["source_file"])
			require.Equal(t, map[string]any{"values": []any{float64(1), true}}, store.accepted[0].Document.Metadata["nested"])
		})
	}
}

func TestMultipartContentTypeIsCaseInsensitive(t *testing.T) {
	store := &validationAcceptStore{}
	r := validationMultipartRequest(t, nil)
	r.Header.Set("Content-Type", strings.Replace(r.Header.Get("Content-Type"), "multipart/form-data", "Multipart/Form-Data", 1))
	rec := httptest.NewRecorder()
	validationIngestHandler(store)(rec, r)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Len(t, store.accepted, 1)
}

type validationVectorStore struct {
	domain.VectorStore
	topK   int
	filter domain.VectorFilter
}

func (s *validationVectorStore) QueryFiltered(_ context.Context, _ []float32, topK int, filter domain.VectorFilter) ([]domain.ScoredChunk, error) {
	s.topK, s.filter = topK, filter
	return nil, nil
}

func TestMaxChunksIsClamped(t *testing.T) {
	for _, tc := range []struct{ requested, configured, want int }{
		{0, 3, 3}, {2, 3, 2}, {10, 3, 3}, {100, 1, 1},
	} {
		store := &validationVectorStore{}
		cfg := &config.Config{MaxChunksPerQuery: tc.configured}
		h := QueryHandler(retriever.NewRetriever(store, embedder.NewFakeEmbedder(256), 0.7, 10), nil, cfg, nil)
		body, err := json.Marshal(QueryRequest{Query: "archive", Mode: "global", MaxChunks: tc.requested, FilterDocType: " cafe\u0301 "})
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		h(rec, validationJSONRequest(t, "/query", string(body)))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, tc.want, store.topK)
		require.Equal(t, "café", store.filter.DocType)
	}
}

func TestStrictJSONBodyLimitsAndSyntax(t *testing.T) {
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"query":"archive"} `, 200},
		{`{"query":"archive"} {}`, 400},
		{`{"query":"archive"} garbage`, 400},
		{`{"query":"archive","allowed_document_ids":[}`, 400},
		{`{"query":"archive"` + strings.Repeat(" ", 1<<20), 413},
		{`{"query":"archive","unknown":true}` + strings.Repeat(" ", 1<<20), 413},
	} {
		rec := httptest.NewRecorder()
		validationQueryHandler(10)(rec, validationJSONRequest(t, "/query", tc.body))
		require.Equal(t, tc.want, rec.Code, rec.Body.String())
	}
	rec := httptest.NewRecorder()
	body, err := json.Marshal(QueryRequest{Query: strings.Repeat("é", 5001)})
	require.NoError(t, err)
	validationQueryHandler(10)(rec, validationJSONRequest(t, "/query", string(body)))
	require.Equal(t, 400, rec.Code)
	require.Contains(t, rec.Body.String(), "10000 bytes")
}

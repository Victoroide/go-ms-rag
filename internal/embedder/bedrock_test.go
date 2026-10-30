package embedder

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

func bedrockTestEmbedder(t *testing.T, handler http.HandlerFunc) *BedrockEmbedder {
	t.Helper()
	t.Setenv("GO_RAG_FAKE_MODE", "true")
	t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("EMBED_TIMEOUT", "1s")
	t.Setenv("EMBEDDING_DIMENSIONS", "256")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewBedrockEmbedder(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	e.rt = bedrockruntime.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test-key", "test-secret", ""),
		Retryer:     func() aws.Retryer { return aws.NopRetryer{} },
	}, func(o *bedrockruntime.Options) { o.BaseEndpoint = aws.String(server.URL) })
	return e
}

func TestEmbeddingDimensionMismatchFails(t *testing.T) {
	for _, size := range []int{0, 255, 257} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			e := bedrockTestEmbedder(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(titanEmbedResponse{Embedding: make([]float32, size)}); err != nil {
					t.Error(err)
				}
			})
			if vector, err := e.Embed(t.Context(), "text"); !errors.Is(err, ErrInvalidEmbedding) || vector != nil {
				t.Fatalf("invalid embedding accepted: length=%d error=%v", len(vector), err)
			}
		})
	}
}

func TestEmbeddingReturnsValidVector(t *testing.T) {
	e := bedrockTestEmbedder(t, func(w http.ResponseWriter, r *http.Request) {
		var request titanEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.InputText != "text" || request.Dimensions != 256 || !request.Normalize {
			t.Errorf("unexpected embedding request: %+v", request)
		}
		vector := make([]float32, 256)
		vector[0], vector[255] = 0.25, -0.5
		if err := json.NewEncoder(w).Encode(titanEmbedResponse{Embedding: vector}); err != nil {
			t.Error(err)
		}
	})
	vector, err := e.Embed(t.Context(), "text")
	if err != nil || len(vector) != 256 || vector[0] != 0.25 || vector[255] != -0.5 {
		t.Fatalf("valid embedding rejected or changed: length=%d error=%v", len(vector), err)
	}
}

func TestEmbeddingPreservesCallerDeadline(t *testing.T) {
	e := bedrockTestEmbedder(t, func(_ http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
			return
		}
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := e.Embed(ctx, "text")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) >= 900*time.Millisecond {
		t.Fatalf("caller deadline not enforced: elapsed=%s error=%v", time.Since(started), err)
	}
}

func TestEmbeddingCallTimesOut(t *testing.T) {
	e := bedrockTestEmbedder(t, func(w http.ResponseWriter, r *http.Request) {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
		case <-timer.C:
			if err := json.NewEncoder(w).Encode(titanEmbedResponse{Embedding: make([]float32, 256)}); err != nil {
				t.Error(err)
			}
		}
	})
	started := time.Now()
	_, err := e.Embed(t.Context(), "text")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) >= 1800*time.Millisecond {
		t.Fatalf("embedding timeout not enforced: elapsed=%s error=%v", time.Since(started), err)
	}
}

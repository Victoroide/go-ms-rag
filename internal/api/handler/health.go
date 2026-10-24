// Package handler contains HTTP handler functions that translate between
// the transport layer and domain logic. No business logic belongs here.
package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/Victoroide/go-ms-rag/internal/domain"
	s3store "github.com/Victoroide/go-ms-rag/internal/store/s3"
)

// HealthStatus represents the health check response body.
type HealthStatus struct {
	Status   string `json:"status"`
	Pinecone string `json:"pinecone"`
	Neon     string `json:"neon"`
	S3       string `json:"s3"`
}

// HealthHandler returns an HTTP handler for GET /health.
func HealthHandler(vs domain.VectorStore, cs domain.ChunkStore, s3Client *s3store.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := HealthStatus{
			Status:   "ok",
			Pinecone: "ok",
			Neon:     "ok",
			S3:       "ok",
		}
		healthy := true

		pcCtx, pcCancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer pcCancel()
		if err := vs.Ping(pcCtx); err != nil {
			log.Printf("health: pinecone probe failed: %v", err)
			status.Pinecone = "unreachable"
			healthy = false
		}

		pgCtx, pgCancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer pgCancel()
		if err := cs.Ping(pgCtx); err != nil {
			log.Printf("health: neon probe failed: %v", err)
			status.Neon = "unreachable"
			healthy = false
		}

		if s3Client != nil {
			s3Ctx, s3Cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer s3Cancel()
			if err := s3Client.Ping(s3Ctx); err != nil {
				log.Printf("health: s3 probe failed: %v", err)
				status.S3 = "unreachable"
				healthy = false
			}
		}

		if !healthy {
			status.Status = "degraded"
		}

		w.Header().Set("Content-Type", "application/json")
		if healthy {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		if err := json.NewEncoder(w).Encode(status); err != nil {
			log.Printf("health: encode response: %v", err)
		}
	}
}

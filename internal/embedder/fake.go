package embedder

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"log"
	"math"
)

// FakeEmbedder produces deterministic, L2-normalized embeddings derived from a
// hash of the input text. Identical text yields an identical vector; different
// text yields a different one. It implements domain.EmbeddingProvider with the
// same Embed surface as BedrockEmbedder, so it is a drop-in replacement for the
// retriever and the embed stage when GO_RAG_FAKE_MODE is enabled. No Bedrock
// calls and zero token cost — intended for local/offline QA only.
type FakeEmbedder struct {
	dimensions int
}

// NewFakeEmbedder creates a deterministic embedder of the given dimension.
// A non-positive dimension falls back to 1024 (the Titan v2 default) so the
// fake vectors line up with a Pinecone index expecting 1024-dim vectors.
func NewFakeEmbedder(dimensions int) *FakeEmbedder {
	if dimensions <= 0 {
		dimensions = 1024
	}
	return &FakeEmbedder{dimensions: dimensions}
}

// Embed returns a deterministic unit vector for text. Satisfies
// domain.EmbeddingProvider.
func (e *FakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, e.dimensions)

	// Seed a SplitMix64 PRNG from the text digest: fully deterministic and
	// dependency-free.
	sum := sha256.Sum256([]byte(text))
	state := binary.LittleEndian.Uint64(sum[0:8]) ^
		binary.LittleEndian.Uint64(sum[8:16]) ^
		binary.LittleEndian.Uint64(sum[16:24]) ^
		binary.LittleEndian.Uint64(sum[24:32])
	if state == 0 {
		state = 0x9E3779B97F4A7C15
	}

	var norm float64
	for i := 0; i < e.dimensions; i++ {
		state += 0x9E3779B97F4A7C15
		z := state
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		z ^= z >> 31
		f := (float64(z>>11)/float64(uint64(1)<<53))*2 - 1 // map to [-1, 1)
		vec[i] = float32(f)
		norm += f * f
	}

	// L2-normalize so cosine similarity behaves like the real (Titan
	// normalize=true) vectors.
	if norm > 0 {
		inv := float32(1.0 / math.Sqrt(norm))
		for i := range vec {
			vec[i] *= inv
		}
	}
	log.Printf("fake_embedder: generated %d-dim deterministic embedding for %d chars", e.dimensions, len(text))
	return vec, nil
}

package integration

import (
	"testing"

	"github.com/Victoroide/go-ms-rag/internal/retriever"
	"github.com/stretchr/testify/assert"
)

func TestQueryLocalMode(t *testing.T) {
	ret := retriever.NewRetriever(nil, nil, 0.70, 10)

	// Single entity query -> LOCAL mode.
	mode := ret.DetermineMode(1, 0.85)
	assert.Equal(t, retriever.RetrievalModeLocal, mode,
		"single entity query with high similarity should use LOCAL mode")
}

func TestQueryGlobalMode(t *testing.T) {
	ret := retriever.NewRetriever(nil, nil, 0.70, 10)

	// Multi-doc query -> GLOBAL mode.
	mode := ret.DetermineMode(3, 0.50)
	assert.Equal(t, retriever.RetrievalModeGlobal, mode,
		"multi-doc query should use GLOBAL mode")
}

func TestQueryFallback(t *testing.T) {
	// Mock Pinecone returns 0 results.
	vs := &mockVectorStore{}
	_ = vs // Fallback logic is tested in retriever_test.go.

	ret := retriever.NewRetriever(nil, nil, 0.70, 10)

	// Low similarity triggers GLOBAL.
	mode := ret.DetermineMode(1, 0.30)
	assert.Equal(t, retriever.RetrievalModeGlobal, mode,
		"very low similarity should trigger GLOBAL mode as fallback")
}

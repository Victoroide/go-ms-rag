package domain

import "context"

// VectorFilter restricts a search by document type and document IDs.
// A nil ID list is unrestricted; an empty non-nil list matches nothing.
type VectorFilter struct {
	DocType     string
	DocumentIDs []string
}

// MatchesNothing reports whether the filter excludes every document.
func (f VectorFilter) MatchesNothing() bool { return f.DocumentIDs != nil && len(f.DocumentIDs) == 0 }

// FilteredVectorStore applies document scope before selecting the top results.
type FilteredVectorStore interface {
	QueryFiltered(ctx context.Context, embedding []float32, topK int, filter VectorFilter) ([]ScoredChunk, error)
}

// DocumentPreviewer loads previews for the supplied document IDs only.
type DocumentPreviewer interface {
	DocumentPreviews(ctx context.Context, ids []string) (map[string]string, error)
}

package domain

import "context"

// DocumentVisibility identifies documents whose chunks may be used as evidence.
type DocumentVisibility interface {
	VisibleDocumentIDs(ctx context.Context, ids []string) (map[string]struct{}, error)
}

package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Visibility uses document records to determine which evidence is available.
type Visibility struct {
	Pool *pgxpool.Pool
}

// VisibleDocumentIDs returns the visible documents among the supplied IDs.
func (v Visibility) VisibleDocumentIDs(ctx context.Context, ids []string) (map[string]struct{}, error) {
	visible := make(map[string]struct{})
	seen := make(map[string]struct{}, len(ids))
	queryIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		parsed, err := uuid.Parse(id)
		if err != nil {
			continue
		}
		canonical := parsed.String()
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		queryIDs = append(queryIDs, canonical)
	}
	if len(queryIDs) == 0 {
		return visible, nil
	}

	// Legacy processing rows with a fingerprint were persisted before a lost
	// final status write. Remove this clause after normalizing them to completed.
	rows, err := v.Pool.Query(ctx, `SELECT d.id::text FROM documents d
		WHERE d.id = ANY($1::uuid[]) AND (d.status = 'completed' OR
		(d.status = 'processing' AND EXISTS (
			SELECT 1 FROM document_fingerprints f WHERE f.document_id = d.id)))`, queryIDs)
	if err != nil {
		return nil, fmt.Errorf("postgres: visible documents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("postgres: scan visible document: %w", err)
		}
		visible[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: visible documents: %w", err)
	}
	return visible, nil
}

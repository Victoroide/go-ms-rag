package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/stretchr/testify/require"
)

const listingFirstID = "00000000-0000-4000-8000-000000000001"
const listingSecondID = "00000000-0000-4000-8000-000000000002"
const listingThirdID = "00000000-0000-4000-8000-000000000003"

func listingDatabase(t *testing.T) (*Client, domain.DocumentLister) {
	t.Helper()
	c, err := NewClient(t.Context(), startTestDatabase(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close(context.Background())) })
	require.NoError(t, c.RunMigrations(t.Context()))
	lister, ok := any(c).(domain.DocumentLister)
	require.True(t, ok, "PostgreSQL must support document summaries")
	for _, id := range []string{listingThirdID, listingFirstID, listingSecondID} {
		_, err = c.Pool.Exec(t.Context(), `INSERT INTO documents(id, content, status) VALUES ($1,'private text','processing')`, id)
		require.NoError(t, err)
	}
	return c, lister
}

func TestStreamDocumentsOrdersByID(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a Docker daemon")
	}
	_, lister := listingDatabase(t)
	var ids []string
	require.NoError(t, lister.StreamDocuments(t.Context(), func(doc domain.DocumentSummary) error {
		ids = append(ids, doc.ID)
		require.Equal(t, "processing", doc.Status)
		require.Equal(t, "document", doc.DocType)
		require.False(t, doc.CreatedAt.IsZero())
		require.False(t, doc.UpdatedAt.IsZero())
		require.Nil(t, doc.CompletedAt)
		return nil
	}))
	require.Equal(t, []string{listingFirstID, listingSecondID, listingThirdID}, ids)
	sentinel := errors.New("stop reading")
	calls := 0
	require.ErrorIs(t, lister.StreamDocuments(t.Context(), func(domain.DocumentSummary) error {
		calls++
		return sentinel
	}), sentinel)
	require.Equal(t, 1, calls)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, lister.StreamDocuments(ctx, func(domain.DocumentSummary) error { return nil }), context.Canceled)
}

func TestListDocumentsPageBoundaries(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a Docker daemon")
	}
	c, lister := listingDatabase(t)
	for _, tc := range []struct {
		after string
		limit int
		ids   []string
	}{
		{"", 2, []string{listingFirstID, listingSecondID}},
		{listingSecondID, 2, []string{listingThirdID}},
		{listingFirstID, 1, []string{listingSecondID}},
		{listingThirdID, 2, []string{}},
	} {
		page, err := lister.ListDocumentsPage(t.Context(), tc.after, tc.limit)
		require.NoError(t, err)
		ids := make([]string, 0, len(page))
		for _, doc := range page {
			ids = append(ids, doc.ID)
		}
		require.Equal(t, tc.ids, ids)
	}
	completed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	_, err := c.Pool.Exec(t.Context(), `UPDATE documents SET status='failed',chunk_count=7,file_name='manual.pdf',file_type='pdf',s3_key='source',last_error='invalid file',completed_at=$2 WHERE id=$1`, listingSecondID, completed)
	require.NoError(t, err)
	doc, err := lister.GetDocumentSummary(t.Context(), domain.DocumentID(listingSecondID))
	require.NoError(t, err)
	require.NotNil(t, doc)
	require.Equal(t, listingSecondID, doc.ID)
	require.Equal(t, "failed", doc.Status)
	require.Equal(t, 7, doc.ChunkCount)
	require.Equal(t, "manual.pdf", *doc.FileName)
	require.Equal(t, "pdf", *doc.FileType)
	require.Equal(t, "source", *doc.S3Key)
	require.Equal(t, "invalid file", *doc.LastError)
	require.True(t, completed.Equal(*doc.CompletedAt))
	doc, err = lister.GetDocumentSummary(t.Context(), "00000000-0000-4000-8000-000000000099")
	require.NoError(t, err)
	require.Nil(t, doc)
	_, err = lister.ListDocumentsPage(t.Context(), "invalid", 1)
	require.Error(t, err)
}

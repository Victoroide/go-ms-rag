package integration

import (
	"testing"

	"github.com/Victoroide/quarry-rag/internal/chunker"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/store/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestInsertDocumentNeverOverwrites(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a Docker daemon")
	}
	ctx := t.Context()
	client, err := postgres.NewClient(ctx, startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close(ctx)) })
	require.NoError(t, client.RunMigrations(ctx))
	fileName, fileType, s3Key := "original.docx", "docx", "original-key"
	doc := domain.Document{
		ID: domain.DocumentID(uuid.NewString()), Content: "Original document", ContentPreview: "Original preview",
		Metadata: map[string]any{"doc_type": "original", "version": "one"}, ChunkCount: 3,
		FileName: &fileName, FileType: &fileType, S3Key: &s3Key,
	}
	require.NoError(t, client.InsertDocument(ctx, doc))
	require.NoError(t, client.UpdateDocumentStatus(ctx, doc.ID, "completed"))
	fp := chunker.Fingerprint(doc.Content)
	require.NoError(t, client.StoreFingerprint(ctx, fp, string(doc.ID)))
	var original []byte
	require.NoError(t, client.Pool.QueryRow(ctx, `SELECT row_to_json(d)::text FROM documents d WHERE id = $1`, string(doc.ID)).Scan(&original))
	for _, replacement := range []domain.Document{doc, {
		ID: doc.ID, Content: "Changed document", ContentPreview: "Changed preview",
		Metadata: map[string]any{"doc_type": "changed"}, ChunkCount: 1,
	}} {
		err = client.InsertDocument(ctx, replacement)
		require.ErrorIs(t, err, domain.ErrDocumentExists, "existing document must reject insertion")
		var stored []byte
		require.NoError(t, client.Pool.QueryRow(ctx, `SELECT row_to_json(d)::text FROM documents d WHERE id = $1`, string(doc.ID)).Scan(&stored))
		require.JSONEq(t, string(original), string(stored))
	}
	id, exists, err := client.CheckFingerprint(ctx, fp)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, doc.ID, id)
}

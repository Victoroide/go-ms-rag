package main

import (
	"testing"

	pgstore "github.com/Victoroide/quarry-rag/internal/store/postgres"
	"github.com/Victoroide/quarry-rag/internal/testutil/pgtest"
	"github.com/stretchr/testify/require"
)

func TestDocumentLookupPreservesRetryJobs(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a Docker daemon")
	}
	pg, err := pgstore.NewClient(t.Context(), pgtest.Start(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Close(t.Context())) })
	require.NoError(t, pg.RunMigrations(t.Context()))
	_, err = pg.Pool.Exec(t.Context(), `INSERT INTO documents(id, content, status) VALUES
        ($1, 'retryable', 'failed'), ($2, 'terminal', 'failed')`, storedID, missingID)
	require.NoError(t, err)
	_, err = pg.Pool.Exec(t.Context(), `INSERT INTO ingest_jobs(document_id) VALUES ($1)`, storedID)
	require.NoError(t, err)
	states, err := lookupDocuments(t.Context(), pg, []string{storedID, missingID, "invalid", "00000000-0000-4000-8000-000000000099"})
	require.NoError(t, err)
	require.Equal(t, map[string]documentState{storedID: {Status: "failed", HasJob: true}, missingID: {Status: "failed"}}, states)
	require.Equal(t, []string{"terminal"}, selectOrphanVectors([]vectorRef{{"retry", storedID}, {"terminal", missingID}}, states, 100))
}

package postgres

import (
	"testing"

	"github.com/Victoroide/quarry-rag/internal/testutil/pgtest"
)

func startTestDatabase(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("requires a Docker daemon")
	}
	return pgtest.Start(t)
}

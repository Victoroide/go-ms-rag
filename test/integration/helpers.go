// Package integration exercises service boundaries and persistent storage.
package integration

import (
	"testing"

	"github.com/Victoroide/quarry-rag/internal/testutil/pgtest"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	return pgtest.Start(t)
}

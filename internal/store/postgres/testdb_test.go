package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func startTestDatabase(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("requires a Docker daemon")
	}
	container, err := postgres.Run(t.Context(), "postgres:16-alpine",
		postgres.WithDatabase("rag"), postgres.WithUsername("rag"), postgres.WithPassword("rag"),
		postgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	url, err := container.ConnectionString(t.Context(), "sslmode=disable")
	require.NoError(t, err)
	return url
}

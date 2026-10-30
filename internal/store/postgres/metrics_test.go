package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPendingIngestJobsUsesPersistedState(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a Docker daemon")
	}
	c := jobsDatabase(t)
	count, err := c.PendingIngestJobs(t.Context())
	require.NoError(t, err)
	require.Zero(t, count)
	jobsAccept(t, c, "pending document")
	jobsAccept(t, c, "another pending document")
	count, err = c.PendingIngestJobs(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
	job, err := c.ClaimJob(t.Context(), "metrics", time.Minute, 5)
	require.NoError(t, err)
	require.NotNil(t, job)
	count, err = c.PendingIngestJobs(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.NoError(t, c.RetryJob(t.Context(), *job, time.Minute, "temporary"))
	count, err = c.PendingIngestJobs(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
}

package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/metrics"
	"github.com/stretchr/testify/require"
)

type metricQueryStage struct {
	id  StageID
	run func(*QueryContext) error
}

func (s metricQueryStage) ID() StageID                                   { return s.id }
func (s metricQueryStage) Run(_ context.Context, qc *QueryContext) error { return s.run(qc) }

func TestPipelineMetricsIncludeFailedStagesAndEffectiveMode(t *testing.T) {
	m := metrics.NewService(nil)
	ctx := metrics.WithService(t.Context(), m)
	_, err := RunIngestionPipeline(ctx, NewIngestContext("doc", "text", nil), []Stage{ingestMetricStage{}})
	require.Error(t, err)
	qc := &QueryContext{}
	_, err = RunRetrievalPipeline(ctx, qc, []QueryStage{
		metricQueryStage{StageRetrievalRouting, func(qc *QueryContext) error { qc.RetrievalMode = "LOCAL"; return nil }},
		metricQueryStage{StageRetrieval, func(qc *QueryContext) error { qc.RetrievalMode = "GLOBAL"; return nil }},
		metricQueryStage{StageSynthesis, func(*QueryContext) error { return errors.New("unavailable") }},
	})
	require.Error(t, err)
	var b strings.Builder
	require.NoError(t, m.Registry.WriteText(&b))
	for _, stage := range []string{"embed", "retrieval_routing", "retrieval", "synthesis"} {
		require.Contains(t, b.String(), `quarry_pipeline_stage_duration_seconds_count{stage="`+stage+`"} 1`)
	}
	require.Contains(t, b.String(), `quarry_query_mode_total{mode="GLOBAL"} 1`)
	require.NotContains(t, b.String(), `quarry_query_mode_total{mode="LOCAL"}`)
}

type ingestMetricStage struct{}

func (ingestMetricStage) ID() StageID                               { return StageEmbed }
func (ingestMetricStage) Run(context.Context, *IngestContext) error { return errors.New("unavailable") }

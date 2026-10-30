package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/llm"
	"github.com/Victoroide/quarry-rag/internal/retriever"
	"github.com/Victoroide/quarry-rag/internal/store/memory"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/require"
)

type synthesisDeadlineClient struct {
	llm.FakeConverser
	calls int
	call  func(context.Context) (*bedrockruntime.ConverseOutput, error)
}

func (c *synthesisDeadlineClient) Converse(ctx context.Context, _ []brtypes.Message) (*bedrockruntime.ConverseOutput, error) {
	c.calls++
	return c.call(ctx)
}
func (c *synthesisDeadlineClient) ConverseWithModelAndSystem(ctx context.Context, _, _ string, _ []brtypes.Message, _ float32, _ int) (*bedrockruntime.ConverseOutput, error) {
	c.calls++
	return c.call(ctx)
}
func synthesisDeadlineConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{BedrockFallbackModel: "fallback", SynthesisTimeout: 20 * time.Millisecond, MinRelevanceScore: .8}
}
func TestMinimumRelevanceScoreSkipsSynthesis(t *testing.T) {
	cfg := synthesisDeadlineConfig(t)
	client := &synthesisDeadlineClient{call: func(context.Context) (*bedrockruntime.ConverseOutput, error) {
		return nil, errors.New("must not synthesize")
	}}
	amb := llm.NewAmbassador(cfg)
	amb.SetClient(client)
	qc := &QueryContext{OriginalQuery: "archive", RetrievedChunks: []domain.ScoredChunk{{Chunk: domain.Chunk{Text: "weak evidence"}, Score: .79}}}
	require.NoError(t, NewSynthesisStage(amb, cfg).Run(t.Context(), qc))
	require.Equal(t, 0, client.calls)
	require.True(t, qc.EarlyExit)
	require.Empty(t, qc.RetrievedChunks)
	require.Equal(t, "No relevant information was found to answer this question.", qc.FinalAnswer)
	require.Equal(t, "no_evidence", qc.FinishReason)
}
func TestSynthesisIsCalledOncePerAttemptPolicy(t *testing.T) {
	for _, routed := range []bool{false, true} {
		t.Run(fmt.Sprint(routed), func(t *testing.T) {
			cfg := synthesisDeadlineConfig(t)
			client := &synthesisDeadlineClient{call: func(context.Context) (*bedrockruntime.ConverseOutput, error) {
				return nil, errors.New("ThrottlingException")
			}}
			amb := llm.NewAmbassador(cfg)
			amb.SetClient(client)
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			if routed {
				qc := &QueryContext{OriginalQuery: "archive", RetrievedChunks: []domain.ScoredChunk{{Chunk: domain.Chunk{Text: "evidence"}, Score: .9}}}
				require.Error(t, NewSynthesisStage(amb, cfg).Run(ctx, qc))
			} else {
				_, err := amb.ConverseRoutedPlain(ctx, "fallback", "", nil, 0, 1024)
				require.Error(t, err)
			}
			require.Equal(t, 1, client.calls)
			require.NoError(t, ctx.Err(), "application retry waited for the deadline")
		})
	}
}
func TestSynthesisTimeoutCancelsUpstream(t *testing.T) {
	cfg := synthesisDeadlineConfig(t)
	client := &synthesisDeadlineClient{call: func(ctx context.Context) (*bedrockruntime.ConverseOutput, error) { <-ctx.Done(); return nil, ctx.Err() }}
	amb := llm.NewAmbassador(cfg)
	amb.SetClient(client)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	qc := &QueryContext{OriginalQuery: "archive", RetrievedChunks: []domain.ScoredChunk{{Chunk: domain.Chunk{Text: "evidence"}, Score: .9}}}
	require.ErrorIs(t, NewSynthesisStage(amb, cfg).Run(ctx, qc), context.DeadlineExceeded)
	require.NoError(t, ctx.Err(), "synthesis exhausted the whole request budget")
}
func TestQueryTextIsNotLogged(t *testing.T) {
	var buf bytes.Buffer
	old, flags := slog.Default(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer func() { slog.SetDefault(old); log.SetFlags(flags) }()
	qc := &QueryContext{OriginalQuery: "private archive question 927"}
	store := memory.NewFakeVectorStore()
	require.NoError(t, store.Upsert(t.Context(), "", []domain.Chunk{{ID: "chunk", DocumentID: "document", Text: "Archive evidence", Embedding: []float32{1, 0}}}))
	ret := retriever.NewRetriever(store, &scopeCountingEmbedder{}, .7, 10)
	cfg := synthesisDeadlineConfig(t)
	amb := llm.NewAmbassador(cfg)
	amb.SetClient(llm.NewFakeConverser())
	_, err := RunRetrievalPipeline(t.Context(), qc, []QueryStage{NewDecomposeStage(), NewRoutingStage(ret), NewRetrievalStage(ret), NewSynthesisStage(amb, cfg)})
	require.NoError(t, err)
	require.NotEmpty(t, qc.FinalAnswer)
	require.NotContains(t, buf.String(), qc.OriginalQuery)
	require.Contains(t, buf.String(), "query_bytes=28")
	digest := sha256.Sum256([]byte(qc.OriginalQuery))
	require.Contains(t, buf.String(), fmt.Sprintf("%x", digest[:6]))
	require.NotContains(t, buf.String(), "private archive")
	require.NotContains(t, buf.String(), "Archive evidence")
}

func TestDefaultMinimumRelevancePreservesRetrievedEvidence(t *testing.T) {
	cfg := synthesisDeadlineConfig(t)
	cfg.MinRelevanceScore = 0
	client := &synthesisDeadlineClient{call: func(context.Context) (*bedrockruntime.ConverseOutput, error) {
		return &bedrockruntime.ConverseOutput{StopReason: brtypes.StopReasonEndTurn, Output: &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{Role: brtypes.ConversationRoleAssistant, Content: []brtypes.ContentBlock{&brtypes.ContentBlockMemberText{Value: "answer"}}}}}, nil
	}}
	amb := llm.NewAmbassador(cfg)
	amb.SetClient(client)
	qc := &QueryContext{OriginalQuery: "archive", RetrievedChunks: []domain.ScoredChunk{{Chunk: domain.Chunk{Text: "authorized evidence"}, Score: -.5}}}
	require.NoError(t, NewSynthesisStage(amb, cfg).Run(t.Context(), qc))
	require.Equal(t, "answer", qc.FinalAnswer)
	require.Equal(t, 1, qc.ContextChunks)
	require.Equal(t, 1, client.calls)
	require.False(t, qc.EarlyExit)
}

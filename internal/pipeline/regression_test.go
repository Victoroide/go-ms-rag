package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/embedder"
	"github.com/Victoroide/quarry-rag/internal/retriever"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/llm"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

type promptRecorder struct {
	llm.FakeConverser
	system, prompt string
}

func (r *promptRecorder) ConverseWithModelAndSystem(ctx context.Context, model, system string, messages []brtypes.Message, temp float32, maxTokens int) (*bedrockruntime.ConverseOutput, error) {
	r.system = system
	r.prompt = messages[0].Content[0].(*brtypes.ContentBlockMemberText).Value
	return r.FakeConverser.ConverseWithModelAndSystem(ctx, model, system, messages, temp, maxTokens)
}
func TestSynthesisUsesQuestionLanguageWithContext(t *testing.T) {
	for _, chunks := range [][]domain.ScoredChunk{{{Chunk: domain.Chunk{Text: "The policy lasts for three years."}, Score: 1}}} {
		r := &promptRecorder{}
		cfg := &config.Config{BedrockFallbackModel: "fallback"}
		amb := llm.NewAmbassador(cfg)
		amb.SetClient(r)
		qc := &QueryContext{OriginalQuery: "How long does the policy last?", RetrievedChunks: chunks}
		if err := NewSynthesisStage(amb, cfg).Run(t.Context(), qc); err != nil {
			t.Fatal(err)
		}
		if r.system == "" || !strings.Contains(strings.ToLower(r.system), "language of the user's question") {
			t.Fatalf("missing question language instruction: %q", r.system)
		}
		if !strings.Contains(r.prompt, qc.OriginalQuery) {
			t.Fatal("question omitted from synthesis")
		}
	}
}
func TestExtractNamedEntityUnicodeAndConnectors(t *testing.T) {
	for _, tt := range []struct{ text, want string }{{"Érable Central stores reports", "érable central"}, {"Cedar de la", ""}} {
		if got := ExtractNamedEntity(tt.text); got != tt.want {
			t.Errorf("%q: got %q want %q", tt.text, got, tt.want)
		}
	}
}
func TestDecompositionWithoutDatabase(t *testing.T) {
	qc := &QueryContext{OriginalQuery: "List all documents"}
	if err := NewDecomposeStage().Run(t.Context(), qc); err != nil {
		t.Fatal(err)
	}
	if len(qc.SubQueries) != 2 {
		t.Fatalf("expected aggregate routing, got %v", qc.SubQueries)
	}
}

func TestLowercaseUnicodeIsNotNamedEntity(t *testing.T) {
	if got := ExtractNamedEntity("érable central writes reports"); got != "" {
		t.Fatalf("lowercase text treated as proper name: %q", got)
	}
}

func TestSynthesisWithoutEvidenceNeverCallsModel(t *testing.T) {
	qc := &QueryContext{OriginalQuery: "What is in the archive?", TokenUsage: domain.TokenUsage{TotalTokens: 12}, ContextTokens: 12}
	if err := NewSynthesisStage(nil, nil).Run(t.Context(), qc); err != nil {
		t.Fatal(err)
	}
	if qc.FinalAnswer != "No relevant information was found to answer this question." {
		t.Fatalf("unexpected answer: %q", qc.FinalAnswer)
	}
	if qc.ContextTokens != 0 || qc.ContextChunks != 0 || qc.TokenUsage != (domain.TokenUsage{}) {
		t.Fatalf("empty evidence used tokens: %+v", qc)
	}
}
func TestMultiNeedSignalsUseWordBoundaries(t *testing.T) {
	for _, tt := range []struct {
		query string
		want  bool
	}{{"all documents", true}, {"which reports exist?", true}, {"compare archives", true}, {"todos los informes", true}, {"small archive", false}, {"install software", false}, {"call support", false}, {"fitness guide", false}} {
		if got := hasMultiNeedSignal(tt.query); got != tt.want {
			t.Errorf("%q got %v want %v", tt.query, got, tt.want)
		}
	}
}

type failedProbeStore struct{ domain.VectorStore }

func (failedProbeStore) QueryAllNamespaces(context.Context, []float32, int, string) ([]domain.ScoredChunk, error) {
	return nil, errors.New("temporary probe failure")
}
func TestRoutingProbeFailureOverridesAutomaticEntityRouting(t *testing.T) {
	ret := retriever.NewRetriever(failedProbeStore{}, embedder.NewFakeEmbedder(256), 0, 10)
	qc := &QueryContext{OriginalQuery: "Cedar Archive", SubQueries: []string{"Cedar Archive"}, NamedEntity: "cedar archive"}
	if err := NewRoutingStage(ret).Run(t.Context(), qc); err != nil {
		t.Fatal(err)
	}
	if qc.RetrievalMode != "GLOBAL" || len(qc.QueryEmbedding) == 0 {
		t.Fatalf("failed probe did not retain embedding for global retrieval: %+v", qc)
	}
}

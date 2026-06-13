package llm

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// FakeConverser is an offline ModelConverser for GO_RAG_FAKE_MODE. It returns a
// deterministic, grounded answer assembled from the context the synthesis stage
// already packed into the user message, with no Bedrock calls. It implements the
// full Converser + ModelConverser surface (plus Ping) so the Ambassador's
// routing/retry path works unchanged via SetClient. For local/offline QA only.
type FakeConverser struct{}

// NewFakeConverser creates the offline converser.
func NewFakeConverser() *FakeConverser { return &FakeConverser{} }

// Converse satisfies Converser.
func (f *FakeConverser) Converse(_ context.Context, messages []brtypes.Message) (*bedrockruntime.ConverseOutput, error) {
	return f.synthesize(messages), nil
}

// ConverseWithModel satisfies ModelConverser.
func (f *FakeConverser) ConverseWithModel(_ context.Context, _ string, messages []brtypes.Message, _ float32, _ int) (*bedrockruntime.ConverseOutput, error) {
	return f.synthesize(messages), nil
}

// ConverseWithModelAndSystem satisfies ModelConverser.
func (f *FakeConverser) ConverseWithModelAndSystem(_ context.Context, _, _ string, messages []brtypes.Message, _ float32, _ int) (*bedrockruntime.ConverseOutput, error) {
	return f.synthesize(messages), nil
}

// Ping satisfies the optional Pinger interface used by Ambassador.Ping.
func (f *FakeConverser) Ping(_ context.Context) error { return nil }

func (f *FakeConverser) synthesize(messages []brtypes.Message) *bedrockruntime.ConverseOutput {
	var user strings.Builder
	for _, m := range messages {
		for _, b := range m.Content {
			if t, ok := b.(*brtypes.ContentBlockMemberText); ok {
				user.WriteString(t.Value)
				user.WriteString("\n")
			}
		}
	}
	prompt := user.String()
	answer := fmt.Sprintf(
		"[FAKE MODE] Respuesta determinista basada en el contexto recuperado.\n\n%s",
		groundedSummary(prompt),
	)
	in := int32(EstimateTokens(prompt))
	out := int32(EstimateTokens(answer))
	log.Printf("fake_llm: deterministic synthesis (%d chars)", len(answer))

	return &bedrockruntime.ConverseOutput{
		Output: &brtypes.ConverseOutputMemberMessage{
			Value: brtypes.Message{
				Role: brtypes.ConversationRoleAssistant,
				Content: []brtypes.ContentBlock{
					&brtypes.ContentBlockMemberText{Value: answer},
				},
			},
		},
		Usage: &brtypes.TokenUsage{
			InputTokens:  aws.Int32(in),
			OutputTokens: aws.Int32(out),
			TotalTokens:  aws.Int32(in + out),
		},
	}
}

// groundedSummary echoes back the most relevant non-empty context lines so the
// fake answer is provably grounded in retrieval without invoking an LLM.
func groundedSummary(prompt string) string {
	var b strings.Builder
	n := 0
	for _, line := range strings.Split(prompt, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(l)
		b.WriteString("\n")
		n++
		if n >= 12 {
			break
		}
	}
	if b.Len() == 0 {
		return "Sin contexto recuperado."
	}
	return strings.TrimRight(b.String(), "\n")
}

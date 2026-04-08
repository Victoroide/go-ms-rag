package unit

import (
	"testing"

	"github.com/Victoroide/go-ms-rag/internal/pipeline"
	"github.com/Victoroide/go-ms-rag/internal/retriever"
	"github.com/stretchr/testify/assert"
)

func TestExtractNamedEntity(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"person name", "Laura Benitez - Senior Software Engineer", "laura benitez"},
		{"person name with connector", "Maria de la Cruz - Backend", "maria de la cruz"},
		{"job title ignored", "Job Opening: Senior Backend Engineer", ""},
		{"empty string", "", ""},
		{"generic words only", "Senior Backend Engineer with Python", ""},
		{"single capitalised word", "Laura is an engineer", ""},
		{"two capitalised words", "Andres Castillo worked at Google", "andres castillo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pipeline.ExtractNamedEntity(tt.text)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDetermineModeWithQuery(t *testing.T) {
	ret := retriever.NewRetriever(nil, nil, 0.70, 10)

	tests := []struct {
		name          string
		subQueryCount int
		topScore      float64
		namedEntity   string
		want          retriever.RetrievalMode
	}{
		{"multi sub-queries -> GLOBAL", 3, 0.90, "", retriever.RetrievalModeGlobal},
		{"named entity single query -> LOCAL", 1, 0.30, "laura benitez", retriever.RetrievalModeLocal},
		{"high score no entity -> LOCAL", 1, 0.85, "", retriever.RetrievalModeLocal},
		{"low score no entity -> GLOBAL", 1, 0.50, "", retriever.RetrievalModeGlobal},
		{"at threshold -> LOCAL", 1, 0.70, "", retriever.RetrievalModeLocal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ret.DetermineModeWithQuery("test query", tt.subQueryCount, tt.topScore, tt.namedEntity)
			assert.Equal(t, tt.want, got)
		})
	}
}

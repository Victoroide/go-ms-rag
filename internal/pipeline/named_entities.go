package pipeline

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
)

// stopWords are common English words that should not be treated as named entities
// even when capitalised (e.g. at the start of a sentence or title).
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true,
	"for": true, "in": true, "on": true, "at": true, "to": true,
	"of": true, "with": true, "by": true, "from": true, "is": true,
	"are": true, "was": true, "were": true, "be": true, "been": true,
	"job": true, "opening": true, "position": true, "role": true,
	"senior": true, "junior": true, "mid-level": true, "lead": true,
	"engineer": true, "developer": true, "manager": true, "architect": true,
	"software": true, "backend": true, "frontend": true, "fullstack": true,
	"data": true, "devops": true, "cloud": true, "infrastructure": true,
	"company": true, "requirements": true, "experience": true, "skills": true,
	"years": true, "team": true, "about": true, "what": true, "how": true,
	"which": true, "who": true, "find": true, "list": true, "match": true,
	"describe": true, "tell": true, "compare": true, "best": true,
}

// isStopWord returns true if the lowercased word is a generic stop word.
func isStopWord(word string) bool {
	// Strip trailing punctuation before checking.
	clean := strings.TrimRight(strings.ToLower(word), ":;,.-!?")
	return stopWords[clean]
}

// ExtractNamedEntity extracts the first proper noun phrase (person or company
// name) from a content preview.  It requires at least two consecutive
// capitalised, non-stop words so that generic titles like "Job Opening" or
// "Senior Backend Engineer" are ignored.
func ExtractNamedEntity(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	words := strings.Fields(text)

	i := 0
	for i < len(words) {
		word := words[i]

		// Skip stop words and non-capitalised words.
		if isStopWord(word) || !isCapitalised(word) {
			i++
			continue
		}

		// Start collecting a candidate entity phrase.
		var entityWords []string
		entityWords = append(entityWords, word)
		i++

		for i < len(words) {
			next := words[i]
			lower := strings.ToLower(next)
			// Allow name connectors (de, del, la, von).
			isConnector := lower == "de" || lower == "del" || lower == "la" || lower == "von"
			if isConnector || (isCapitalised(next) && !isStopWord(next)) {
				entityWords = append(entityWords, next)
				i++
			} else {
				break
			}
		}

		// Only accept phrases of 2+ non-connector words as proper entities.
		if len(entityWords) >= 2 {
			return strings.ToLower(strings.Join(entityWords, " "))
		}

		// Single capitalised word — skip, too ambiguous.
		entityWords = nil
	}

	return ""
}

// isCapitalised returns true if the first rune of word is an uppercase letter.
func isCapitalised(word string) bool {
	if word == "" {
		return false
	}
	r := rune(word[0])
	return unicode.IsLetter(r) && unicode.IsUpper(r)
}

// entityCache provides a TTL cache for named entities
type entityCache struct {
	mu        sync.RWMutex
	entities  map[string]bool
	fetchedAt time.Time
	ttl       time.Duration
}

var globalEntityCache = &entityCache{
	entities: make(map[string]bool),
	ttl:      60 * time.Second, // 60 seconds TTL
}

// FetchNamedEntitiesFromDB retrieves all named entities from document previews
// Uses a 60-second TTL cache to avoid database round-trips on every query
func FetchNamedEntitiesFromDB(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	// Check cache first
	globalEntityCache.mu.RLock()
	if time.Since(globalEntityCache.fetchedAt) < globalEntityCache.ttl && len(globalEntityCache.entities) > 0 {
		// Cache hit - return a copy to avoid concurrent modification issues
		entities := make(map[string]bool)
		for k, v := range globalEntityCache.entities {
			entities[k] = v
		}
		globalEntityCache.mu.RUnlock()
		return entities, nil
	}
	globalEntityCache.mu.RUnlock()

	// Cache miss - fetch from database using the existing pool
	rows, err := pool.Query(ctx, "SELECT content_preview FROM documents WHERE content_preview IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("query previews: %w", err)
	}
	defer rows.Close()

	entities := make(map[string]bool)
	for rows.Next() {
		var preview string
		if err := rows.Scan(&preview); err != nil {
			log.Printf("WARNING: scan preview: %v", err)
			continue
		}

		entity := ExtractNamedEntity(preview)
		if entity != "" {
			entities[entity] = true
			// Add individual words only for proper noun phrases (person names).
			// Each word of "laura benitez" is useful; generic words are already
			// filtered out by ExtractNamedEntity's stop-word list.
			for _, word := range strings.Fields(entity) {
				if len(word) > 2 {
					entities[word] = true
				}
			}
		}
	}

	// Update cache
	globalEntityCache.mu.Lock()
	globalEntityCache.entities = entities
	globalEntityCache.fetchedAt = time.Now()
	globalEntityCache.mu.Unlock()

	log.Printf("fetchNamedEntities: cached %d entities (TTL: %s)", len(entities), globalEntityCache.ttl)
	return entities, nil
}

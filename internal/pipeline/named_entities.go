package pipeline

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

// stopWords are common English words that should not be treated as named entities
// even when capitalised (e.g. at the start of a sentence or title).
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true,
	"for": true, "in": true, "on": true, "at": true, "to": true,
	"of": true, "with": true, "by": true, "from": true, "is": true,
	"are": true, "was": true, "were": true, "be": true, "been": true,
	"document": true, "reference": true, "guide": true, "report": true,
	"title": true, "section": true, "summary": true, "overview": true,
	"what": true, "how": true, "which": true, "who": true, "find": true, "list": true,

	"describe": true, "tell": true, "compare": true, "best": true,
}

// isStopWord returns true if the lowercased word is a generic stop word.
func isStopWord(word string) bool {
	// Strip trailing punctuation before checking.
	clean := strings.TrimRight(strings.ToLower(word), ":;,.-!?")
	return stopWords[clean]
}

// ExtractNamedEntity finds a proper noun phrase with at least two capitalised,
// non-stop words so generic document titles are ignored.
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

		// Start collecting an entity phrase.
		var entityWords []string
		entityWords = append(entityWords, word)
		properWords := 1
		i++

		for i < len(words) {
			next := words[i]
			lower := strings.ToLower(next)
			// Allow name connectors (de, del, la, von).
			isConnector := lower == "de" || lower == "del" || lower == "la" || lower == "von"
			if isConnector || (isCapitalised(next) && !isStopWord(next)) {
				entityWords = append(entityWords, next)
				if !isConnector {
					properWords++
				}
				i++
			} else {
				break
			}
		}

		// Only accept phrases of 2+ non-connector words as proper entities.
		if properWords >= 2 {
			return strings.ToLower(strings.Join(entityWords, " "))
		}

		// Single capitalised word — skip, too ambiguous.
	}

	return ""
}

// isCapitalised returns true if the first rune of word is an uppercase letter.
func isCapitalised(word string) bool {
	if word == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(word)
	return unicode.IsLetter(r) && unicode.IsUpper(r)
}

// entityCache provides a TTL cache for named entities
type entityCache struct {
	mu        sync.RWMutex
	entities  map[string]bool
	fetchedAt time.Time
	pool      *pgxpool.Pool
	ttl       time.Duration
}

var globalEntityCache = &entityCache{
	entities: make(map[string]bool),
	ttl:      60 * time.Second, // 60 seconds TTL
}

// FetchNamedEntitiesFromDB retrieves all named entities from document previews
// Uses a 60-second TTL cache to avoid database round-trips on every query
func FetchNamedEntitiesFromDB(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	if pool == nil {
		return map[string]bool{}, nil
	}
	// Check cache first
	globalEntityCache.mu.RLock()
	if globalEntityCache.pool == pool && !globalEntityCache.fetchedAt.IsZero() && time.Since(globalEntityCache.fetchedAt) < globalEntityCache.ttl {
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
			// Individual words also help match abbreviated references to named entities.
			// Each word of "cedar archive" is useful; generic words are already
			// filtered out by ExtractNamedEntity's stop-word list.
			for _, word := range strings.Fields(entity) {
				if len(word) > 2 {
					entities[word] = true
				}
			}
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read previews: %w", err)
	}

	// Update cache
	globalEntityCache.mu.Lock()
	globalEntityCache.pool = pool
	globalEntityCache.entities = entities
	globalEntityCache.fetchedAt = time.Now()
	globalEntityCache.mu.Unlock()

	log.Printf("fetchNamedEntities: cached %d entities (TTL: %s)", len(entities), globalEntityCache.ttl)
	return entities, nil
}

package pipeline

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Victoroide/go-ms-rag/internal/config"
	"github.com/Victoroide/go-ms-rag/internal/store/postgres"
)

// ExtractNamedEntity extracts the first meaningful noun phrase from text
// For candidate profiles and job descriptions, this is typically the person's name or company name
func ExtractNamedEntity(text string) string {
	// Clean up the text
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// Split into words
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}

	// Look for patterns that suggest names:
	// - Words starting with capital letters
	// - Multiple consecutive capitalized words (e.g., "Laura Benitez")
	// - Company names with special patterns (e.g., "NVision-X", "DataFlow")

	var entityWords []string
	i := 0

	for i < len(words) {
		word := words[i]

		// Check if word looks like a name (starts with capital and contains letters)
		if word != "" && unicode.IsLetter(rune(word[0])) && string(word[0]) == strings.ToUpper(string(word[0])) {
			// Start collecting potential entity words
			entityWords = append(entityWords, word)
			i++

			// Look for more capitalized words (for multi-word names)
			for i < len(words) {
				nextWord := words[i]
				// Continue if next word is also capitalized or is a common name connector
				if nextWord != "" && unicode.IsLetter(rune(nextWord[0])) &&
					(string(nextWord[0]) == strings.ToUpper(string(nextWord[0])) ||
						strings.ToLower(nextWord) == "de" ||
						strings.ToLower(nextWord) == "del" ||
						strings.ToLower(nextWord) == "la" ||
						strings.ToLower(nextWord) == "von") {
					entityWords = append(entityWords, nextWord)
					i++
				} else {
					break
				}
			}

			// If we collected 2+ words or the word has special characters (like DataFlow), return it
			entity := strings.Join(entityWords, " ")
			if len(entityWords) >= 2 || strings.ContainsAny(entity, "-") || strings.ContainsAny(entity, "Flow") {
				return strings.ToLower(entity)
			}

			// Reset for next potential entity
			entityWords = nil
		} else {
			i++
		}
	}

	// If no multi-word entity found, return the first capitalized word in lowercase
	for _, word := range words {
		if word != "" && unicode.IsLetter(rune(word[0])) && string(word[0]) == strings.ToUpper(string(word[0])) {
			return strings.ToLower(word)
		}
	}

	return ""
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
func FetchNamedEntitiesFromDB(ctx context.Context, cfg *config.Config) (map[string]bool, error) {
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

	// Cache miss - fetch from database
	pgClient, err := postgres.NewClient(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	defer pgClient.Close(ctx)

	rows, err := pgClient.Pool.Query(ctx, "SELECT content_preview FROM documents WHERE content_preview IS NOT NULL")
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
			// Also add individual words for partial matching
			words := strings.Fields(entity)
			for _, word := range words {
				if len(word) > 2 { // Skip very short words
					entities[strings.ToLower(word)] = true
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

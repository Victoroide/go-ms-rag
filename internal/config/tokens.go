package config

import (
	"fmt"
	"log/slog"
	"strings"
	"unicode"
)

func (c *Config) validateServiceTokens() error {
	c.ServiceToken = strings.TrimSpace(c.ServiceToken)
	tokens := make([]string, 0, len(c.ServiceTokens)+1)
	seen := make(map[string]bool, len(c.ServiceTokens)+1)
	shortCount := 0
	for _, token := range append([]string{c.ServiceToken}, c.ServiceTokens...) {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if strings.ContainsFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return fmt.Errorf("config: SERVICE_TOKEN and SERVICE_TOKENS must not contain whitespace or control characters")
		}
		if seen[token] {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
		if len(token) < 32 {
			shortCount++
		}
	}
	if !c.FakeMode && len(tokens) == 0 {
		return fmt.Errorf("config: SERVICE_TOKEN or SERVICE_TOKENS is required when GO_RAG_FAKE_MODE is off")
	}
	c.ServiceTokens = tokens
	if shortCount > 0 {
		slog.Warn("service tokens shorter than 32 bytes", "count", shortCount)
	}
	return nil
}

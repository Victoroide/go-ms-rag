// Package main validates document retrieval queries against the live server.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type queryTest struct {
	name         string
	query        string
	expectedMode string
}

type queryResponse struct {
	Answer     string `json:"answer"`
	Mode       string `json:"mode"`
	ChunksUsed int    `json:"chunks_used"`
}

func main() {
	queries := []queryTest{
		{"R1", "What systems does the archive guide describe?", "LOCAL"},
		{"R2", "Describe the network incident report", "LOCAL"},
		{"C1", "Which documents describe AWS Bedrock?", "GLOBAL"},
		{"C2", "Find documents describing Python and Spark", "GLOBAL"},
		{"C3", "Compare all reports about system recovery", "GLOBAL"},
		{"E1", "Compare reference guides with incident reports", "GLOBAL"},
	}

	baseURL := "http://localhost:8081"
	passed := 0
	failed := 0

	fmt.Println("=== Document Retrieval Query Validation ===")
	fmt.Printf("Testing %d queries against %s\n\n", len(queries), baseURL)

	for _, q := range queries {
		fmt.Printf("[%s] %s\n", q.name, q.query)

		reqBody := map[string]interface{}{
			"query":      q.query,
			"max_chunks": 10,
		}
		bodyBytes, _ := json.Marshal(reqBody)

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/query", bytes.NewReader(bodyBytes))
		if err != nil {
			fmt.Printf("request: %v\n", err)
			failed++
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Service-Token", os.Getenv("SERVICE_TOKEN"))
		resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
		if err != nil {
			fmt.Printf("  ERROR: %v\n", err)
			failed++
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		var result queryResponse
		if err := json.Unmarshal(body, &result); err != nil {
			fmt.Printf("  ERROR: decode response: %v\n", err)
			failed++
			continue
		}

		// Validate
		valid := true
		var issues []string

		if result.ChunksUsed < 1 {
			valid = false
			issues = append(issues, fmt.Sprintf("chunks_used=%d (expected >= 1)", result.ChunksUsed))
		}

		if strings.ToUpper(result.Mode) != q.expectedMode {
			valid = false
			issues = append(issues, fmt.Sprintf("mode=%s (expected %s)", result.Mode, q.expectedMode))
		}

		if strings.Contains(result.Answer, "No relevant information found") {
			valid = false
			issues = append(issues, "answer contains 'No relevant information found'")
		}

		if valid {
			fmt.Printf("  PASS - chunks=%d, mode=%s\n", result.ChunksUsed, result.Mode)
			passed++
		} else {
			fmt.Printf("  FAIL - %s\n", strings.Join(issues, ", "))
			failed++
		}
	}

	fmt.Println("\n=== Results ===")
	fmt.Printf("Passed: %d / %d\n", passed, len(queries))
	fmt.Printf("Failed: %d / %d\n", failed, len(queries))

	if failed > 0 {
		fmt.Println("\nValidation failed")
		os.Exit(1)
	}

	fmt.Println("\nAll validation queries passed!")
}

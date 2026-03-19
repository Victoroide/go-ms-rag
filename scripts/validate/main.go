// Package main validates the 14 TalentBridge queries against the live server.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
		{"R1", "What are Laura's main technical skills?", "LOCAL"},
		{"R2", "Describe Andres Castillo's experience with microservices", "LOCAL"},
		{"R3", "What cloud platforms does Camila Rojas have experience with?", "LOCAL"},
		{"R4", "Tell me about Marco Villanueva's leadership experience", "LOCAL"},
		{"R5", "What is the tech stack for the Senior Backend Engineer role?", "GLOBAL"},
		{"R6", "What are the key responsibilities for the DataFlow Senior Data Engineer position?", "LOCAL"},
		{"R7", "How many years of experience does Laura Benitez have?", "LOCAL"},
		{"C1", "Which candidates have AWS Bedrock experience?", "GLOBAL"},
		{"C2", "Find candidates with Python and Spark skills", "GLOBAL"},
		{"C3", "Who has experience with microservices architecture?", "GLOBAL"},
		{"C4", "List all candidates with cloud computing experience", "GLOBAL"},
		{"E1", "Match candidates to the Senior Backend Engineer role", "GLOBAL"},
		{"E2", "Which candidates are suitable for the DataFlow data engineer position?", "GLOBAL"},
		{"E3", "Find the best candidate for a role requiring Go and gRPC", "GLOBAL"},
	}

	baseURL := "http://localhost:8081"
	passed := 0
	failed := 0

	fmt.Println("=== TalentBridge Query Validation ===")
	fmt.Printf("Testing %d queries against %s\n\n", len(queries), baseURL)

	for _, q := range queries {
		fmt.Printf("[%s] %s\n", q.name, q.query)

		reqBody := map[string]interface{}{
			"query":      q.query,
			"max_chunks": 10,
		}
		bodyBytes, _ := json.Marshal(reqBody)

		resp, err := http.Post(baseURL+"/query", "application/json", bytes.NewReader(bodyBytes))
		if err != nil {
			fmt.Printf("  ERROR: %v\n", err)
			failed++
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

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

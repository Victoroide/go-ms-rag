package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const baseURL = "http://localhost:8081"

// --- response types ---

type healthResp struct {
	Status   string `json:"status"`
	Pinecone string `json:"pinecone"`
	Neon     string `json:"neon"`
	S3       string `json:"s3"`
}

type ingestResp struct {
	DocumentID string `json:"document_id"`
	Status     string `json:"status"`
}

type scoredChunk struct {
	DocID      string  `json:"doc_id"`
	DocType    string  `json:"doc_type"`
	ChunkIndex int     `json:"chunk_index"`
	Score      float64 `json:"score"`
	Text       string  `json:"text"`
}

type queryResp struct {
	Answer       string        `json:"answer"`
	Mode         string        `json:"mode"`
	ChunksUsed   int           `json:"chunks_used"`
	ScoredChunks []scoredChunk `json:"scored_chunks"`
}

type deleteResp struct {
	Deleted bool   `json:"deleted"`
	DocID   string `json:"doc_id"`
}

type fileResp struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expires_in"`
	Error     string `json:"error"`
	Message   string `json:"message"`
}

// --- helpers ---

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

type scenarioResult struct {
	name   string
	pass   bool
	reason string
}

var (
	sofiaID   string
	techJobID string
	passed    int
	total     int
	results   []scenarioResult
)

func doRequest(method, path string, body interface{}) (*http.Response, []byte, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, baseURL+path, reqBody)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, err
	}
	return resp, respBody, nil
}

func printScenario(num int, name, method, path string, reqBody interface{}, status int, respBody []byte, pass bool, reason string) {
	total++
	result := scenarioResult{name: name, pass: pass, reason: reason}
	results = append(results, result)

	fmt.Printf("\n[SCENARIO %d] %s\n", num, name)
	reqStr := ""
	if reqBody != nil {
		b, _ := json.Marshal(reqBody)
		reqStr = " " + truncate(string(b), 120)
	}
	fmt.Printf("REQUEST:  %s %s%s\n", method, path, reqStr)
	fmt.Printf("RESPONSE: %d   %s\n", status, truncate(string(respBody), 300))

	if pass {
		passed++
		fmt.Printf("RESULT:   PASS\n")
	} else {
		fmt.Printf("RESULT:   FAIL   %s\n", reason)
		fmt.Printf("\nFull response body:\n%s\n", string(respBody))
		printSummary()
		os.Exit(1)
	}
}

func printSummary() {
	fmt.Printf("\n════════════════════════════════════════\n")
	fmt.Printf("SUMMARY: %d/%d scenarios passed\n", passed, total)
	fmt.Printf("════════════════════════════════════════\n")
}

func loadDatabaseURL() string {
	// Read .env file manually (no external deps)
	data, err := os.ReadFile(".env")
	if err != nil {
		fmt.Printf("WARNING: could not read .env: %v\n", err)
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "DATABASE_URL=") {
			return strings.TrimPrefix(line, "DATABASE_URL=")
		}
	}
	return ""
}

func isValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

func main() {
	fmt.Println("═══════════════════════════════════════════════")
	fmt.Println("  Django Call Pattern Simulation")
	fmt.Println("═══════════════════════════════════════════════")

	// ── SCENARIO 1 ──
	scenario1()
	// ── SCENARIO 2 ──
	scenario2()
	// ── SCENARIO 3 ──
	scenario3()
	// Wait and verify in Postgres
	verifyPostgres()
	// ── SCENARIO 4 ──
	scenario4()
	// ── SCENARIO 5 ──
	scenario5()
	// ── SCENARIO 6 ──
	scenario6()
	// ── SCENARIO 7 ──
	scenario7()
	// ── SCENARIO 8 ──
	scenario8()
	// ── SCENARIO 9 ──
	scenario9()
	// ── SCENARIO 10 ──
	scenario10()
	// ── SCENARIO 11 ──
	scenario11()
	// ── SCENARIO 12 ──
	scenario12()
	// ── SCENARIO 13 ──
	scenario13()
	// ── SCENARIO 14 ──
	scenario14()

	printSummary()
}

// ── SCENARIO 1: Health check ──
func scenario1() {
	resp, body, err := doRequest("GET", "/health", nil)
	if err != nil {
		printScenario(1, "Health check", "GET", "/health", nil, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	var h healthResp
	_ = json.Unmarshal(body, &h)

	ok := resp.StatusCode == 200 && h.Status == "ok" && h.Pinecone == "ok" && h.Neon == "ok" && h.S3 == "ok"
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d health=%+v", resp.StatusCode, h)
	}
	printScenario(1, "Health check", "GET", "/health", nil, resp.StatusCode, body, ok, reason)
}

// ── SCENARIO 2: Ingest CV via JSON with explicit doc_type ──
func scenario2() {
	reqBody := map[string]interface{}{
		"content":  "Sofia Mamani - Backend Engineer. 4 years. Skills: Python, Django, PostgreSQL, Docker, AWS S3, Redis, REST APIs, JWT. Built REST APIs for fintech at DataPay BO. CS UMSA Bolivia. English B2. Available immediately.",
		"doc_type": "candidate",
	}
	resp, body, err := doRequest("POST", "/ingest", reqBody)
	if err != nil {
		printScenario(2, "Ingest CV with doc_type=candidate", "POST", "/ingest", reqBody, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	var ir ingestResp
	_ = json.Unmarshal(body, &ir)

	ok := (resp.StatusCode == 202 || resp.StatusCode == 200) && isValidUUID(ir.DocumentID)
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d doc_id=%q valid_uuid=%v", resp.StatusCode, ir.DocumentID, isValidUUID(ir.DocumentID))
	}
	sofiaID = ir.DocumentID
	printScenario(2, "Ingest CV with doc_type=candidate", "POST", "/ingest", reqBody, resp.StatusCode, body, ok, reason)
}

// ── SCENARIO 3: Ingest job via JSON with explicit doc_type ──
func scenario3() {
	reqBody := map[string]interface{}{
		"content":  "Job Opening: Backend Developer. Company: TechBolivia. Salary: $2000-3000 USD/month. Required: Python 3+ years, Django, PostgreSQL, Docker, AWS. Nice to have: Redis, Celery, REST APIs. Level: Mid. English: B1 minimum. Start: flexible.",
		"doc_type": "job",
	}
	resp, body, err := doRequest("POST", "/ingest", reqBody)
	if err != nil {
		printScenario(3, "Ingest job with doc_type=job", "POST", "/ingest", reqBody, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	var ir ingestResp
	_ = json.Unmarshal(body, &ir)

	ok := (resp.StatusCode == 202 || resp.StatusCode == 200) && isValidUUID(ir.DocumentID)
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d doc_id=%q valid_uuid=%v", resp.StatusCode, ir.DocumentID, isValidUUID(ir.DocumentID))
	}
	techJobID = ir.DocumentID
	printScenario(3, "Ingest job with doc_type=job", "POST", "/ingest", reqBody, resp.StatusCode, body, ok, reason)
}

// Verify Postgres after ingest
func verifyPostgres() {
	fmt.Println("\n... Waiting 5 seconds for worker to process ...")
	time.Sleep(5 * time.Second)

	dbURL := loadDatabaseURL()
	if dbURL == "" {
		fmt.Println("WARNING: DATABASE_URL not found, skipping Postgres verification")
		return
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Printf("WARNING: could not connect to Postgres: %v\n", err)
		return
	}
	defer pool.Close()

	// Check both documents
	for _, docInfo := range []struct {
		id      string
		expType string
	}{
		{sofiaID, "candidate"},
		{techJobID, "job"},
	} {
		var status, docType string
		err := pool.QueryRow(ctx,
			`SELECT status, doc_type FROM documents WHERE id = $1`, docInfo.id,
		).Scan(&status, &docType)
		if err != nil {
			fmt.Printf("  DB check for %s: ERROR %v\n", docInfo.id, err)
			// If not yet complete, wait more
			fmt.Println("  Waiting 10 more seconds...")
			time.Sleep(10 * time.Second)
			err = pool.QueryRow(ctx,
				`SELECT status, doc_type FROM documents WHERE id = $1`, docInfo.id,
			).Scan(&status, &docType)
			if err != nil {
				fmt.Printf("  DB check retry for %s: ERROR %v\n", docInfo.id, err)
				printScenario(0, "Postgres verify", "", "", nil, 0, []byte(err.Error()), false, "document not found in DB")
				return
			}
		}
		fmt.Printf("  DB: id=%s status=%s doc_type=%s (expected=%s)\n", docInfo.id, status, docType, docInfo.expType)
		if status != "completed" {
			fmt.Printf("  WARNING: status is %q, not 'completed'. Waiting 10 more seconds...\n", status)
			time.Sleep(10 * time.Second)
			_ = pool.QueryRow(ctx,
				`SELECT status, doc_type FROM documents WHERE id = $1`, docInfo.id,
			).Scan(&status, &docType)
			fmt.Printf("  DB retry: id=%s status=%s doc_type=%s\n", docInfo.id, status, docType)
			if status != "completed" {
				printScenario(0, "Postgres verify", "", "", nil, 0, []byte(fmt.Sprintf("status=%s", status)), false, "document not completed after waiting")
				return
			}
		}
		if docType != docInfo.expType {
			printScenario(0, "Postgres verify", "", "", nil, 0, []byte(fmt.Sprintf("doc_type=%s expected=%s", docType, docInfo.expType)), false, "wrong doc_type")
			return
		}
	}
	fmt.Println("  Postgres verification: PASSED")
}

// ── SCENARIO 4: Deduplication check ──
func scenario4() {
	reqBody := map[string]interface{}{
		"content":  "Sofia Mamani - Backend Engineer. 4 years. Skills: Python, Django, PostgreSQL, Docker, AWS S3, Redis, REST APIs, JWT. Built REST APIs for fintech at DataPay BO. CS UMSA Bolivia. English B2. Available immediately.",
		"doc_type": "candidate",
	}
	resp, body, err := doRequest("POST", "/ingest", reqBody)
	if err != nil {
		printScenario(4, "Deduplication check", "POST", "/ingest", reqBody, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	var ir ingestResp
	_ = json.Unmarshal(body, &ir)

	ok := (resp.StatusCode == 200 || resp.StatusCode == 202) &&
		(ir.Status == "already_ingested" || ir.DocumentID == sofiaID)
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d resp_status=%q doc_id=%q sofia_id=%q", resp.StatusCode, ir.Status, ir.DocumentID, sofiaID)
	}
	printScenario(4, "Deduplication check", "POST", "/ingest", reqBody, resp.StatusCode, body, ok, reason)
}

// ── SCENARIO 5: Query with filter_doc_type=candidate ──
func scenario5() {
	reqBody := map[string]interface{}{
		"query":           "Which candidates have Python and Django experience?",
		"mode":            "auto",
		"filter_doc_type": "candidate",
	}
	resp, body, err := doRequest("POST", "/query", reqBody)
	if err != nil {
		printScenario(5, "Query filter_doc_type=candidate", "POST", "/query", reqBody, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	var qr queryResp
	_ = json.Unmarshal(body, &qr)

	ok := resp.StatusCode == 200
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d", resp.StatusCode)
		printScenario(5, "Query filter_doc_type=candidate", "POST", "/query", reqBody, resp.StatusCode, body, false, reason)
		return
	}

	// All scored_chunks must have doc_type=candidate
	for _, sc := range qr.ScoredChunks {
		if sc.DocType != "candidate" {
			ok = false
			reason = fmt.Sprintf("scored_chunk has doc_type=%q, expected candidate", sc.DocType)
			break
		}
	}
	if ok && qr.ChunksUsed < 1 {
		ok = false
		reason = fmt.Sprintf("chunks_used=%d, expected >= 1", qr.ChunksUsed)
	}
	if ok && strings.Contains(qr.Answer, "No relevant information") {
		ok = false
		reason = "answer contains 'No relevant information'"
	}
	printScenario(5, "Query filter_doc_type=candidate", "POST", "/query", reqBody, resp.StatusCode, body, ok, reason)
}

// ── SCENARIO 6: Query with filter_doc_type=job ──
func scenario6() {
	reqBody := map[string]interface{}{
		"query":           "What job openings require Python and offer remote work?",
		"mode":            "auto",
		"filter_doc_type": "job",
	}
	resp, body, err := doRequest("POST", "/query", reqBody)
	if err != nil {
		printScenario(6, "Query filter_doc_type=job", "POST", "/query", reqBody, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	var qr queryResp
	_ = json.Unmarshal(body, &qr)

	ok := resp.StatusCode == 200
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d", resp.StatusCode)
		printScenario(6, "Query filter_doc_type=job", "POST", "/query", reqBody, resp.StatusCode, body, false, reason)
		return
	}

	for _, sc := range qr.ScoredChunks {
		if sc.DocType != "job" {
			ok = false
			reason = fmt.Sprintf("scored_chunk has doc_type=%q, expected job", sc.DocType)
			break
		}
	}
	if ok && qr.ChunksUsed < 1 {
		ok = false
		reason = fmt.Sprintf("chunks_used=%d, expected >= 1", qr.ChunksUsed)
	}
	printScenario(6, "Query filter_doc_type=job", "POST", "/query", reqBody, resp.StatusCode, body, ok, reason)
}

// ── SCENARIO 7: Semantic ranking — job req as query ──
func scenario7() {
	reqBody := map[string]interface{}{
		"query":           "Python 3+ years, Django, PostgreSQL, Docker, AWS. Experience with REST APIs and JWT authentication.",
		"mode":            "global",
		"filter_doc_type": "candidate",
		"max_chunks":      10,
	}
	resp, body, err := doRequest("POST", "/query", reqBody)
	if err != nil {
		printScenario(7, "Semantic ranking (candidate)", "POST", "/query", reqBody, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	var qr queryResp
	_ = json.Unmarshal(body, &qr)

	ok := resp.StatusCode == 200
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d", resp.StatusCode)
		printScenario(7, "Semantic ranking (candidate)", "POST", "/query", reqBody, resp.StatusCode, body, false, reason)
		return
	}

	if len(qr.ScoredChunks) == 0 {
		ok = false
		reason = "scored_chunks is empty"
	}

	// Check sorted by score descending
	if ok && len(qr.ScoredChunks) > 1 {
		sorted := sort.SliceIsSorted(qr.ScoredChunks, func(i, j int) bool {
			return qr.ScoredChunks[i].Score > qr.ScoredChunks[j].Score
		})
		if !sorted {
			ok = false
			reason = "scored_chunks not sorted by score descending"
		}
	}

	// Check each chunk
	if ok {
		for _, sc := range qr.ScoredChunks {
			if sc.DocID == "" || sc.DocType != "candidate" {
				ok = false
				reason = fmt.Sprintf("chunk missing doc_id or wrong doc_type=%q", sc.DocType)
				break
			}
		}
	}

	if ok && len(qr.ScoredChunks) > 0 {
		fmt.Printf("  Top candidate doc_id=%s score=%.4f\n", qr.ScoredChunks[0].DocID, qr.ScoredChunks[0].Score)
	}
	printScenario(7, "Semantic ranking (candidate)", "POST", "/query", reqBody, resp.StatusCode, body, ok, reason)
}

// ── SCENARIO 8: Vacancy recommendations — CV as query ──
func scenario8() {
	reqBody := map[string]interface{}{
		"query":           "Backend engineer with Python, Django, PostgreSQL, Docker, AWS S3. Looking for mid-level positions.",
		"mode":            "global",
		"filter_doc_type": "job",
		"max_chunks":      5,
	}
	resp, body, err := doRequest("POST", "/query", reqBody)
	if err != nil {
		printScenario(8, "Vacancy recommendations (job)", "POST", "/query", reqBody, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	var qr queryResp
	_ = json.Unmarshal(body, &qr)

	ok := resp.StatusCode == 200
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d", resp.StatusCode)
		printScenario(8, "Vacancy recommendations (job)", "POST", "/query", reqBody, resp.StatusCode, body, false, reason)
		return
	}

	// Check doc_type=job
	for _, sc := range qr.ScoredChunks {
		if sc.DocType != "job" {
			ok = false
			reason = fmt.Sprintf("scored_chunk doc_type=%q, expected job", sc.DocType)
			break
		}
	}

	// Check TECHJOB_ID appears
	if ok {
		found := false
		for _, sc := range qr.ScoredChunks {
			if sc.DocID == techJobID {
				found = true
				break
			}
		}
		if !found {
			ok = false
			reason = fmt.Sprintf("techJobID=%s not found in scored_chunks", techJobID)
		}
	}
	printScenario(8, "Vacancy recommendations (job)", "POST", "/query", reqBody, resp.StatusCode, body, ok, reason)
}

// ── SCENARIO 9: Skill gap analysis ──
func scenario9() {
	reqBody := map[string]interface{}{
		"query": "What skills required in the TechBolivia Backend Developer job are not present in Sofia Mamani's profile? List the gaps.",
		"mode":  "global",
	}
	resp, body, err := doRequest("POST", "/query", reqBody)
	if err != nil {
		printScenario(9, "Skill gap analysis", "POST", "/query", reqBody, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	var qr queryResp
	_ = json.Unmarshal(body, &qr)

	ok := resp.StatusCode == 200
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d", resp.StatusCode)
		printScenario(9, "Skill gap analysis", "POST", "/query", reqBody, resp.StatusCode, body, false, reason)
		return
	}

	if qr.ChunksUsed < 2 {
		ok = false
		reason = fmt.Sprintf("chunks_used=%d, expected >= 2", qr.ChunksUsed)
	}
	if ok && qr.Answer == "" {
		ok = false
		reason = "answer is empty"
	}
	printScenario(9, "Skill gap analysis", "POST", "/query", reqBody, resp.StatusCode, body, ok, reason)
}

// ── SCENARIO 10: File retrieval URL ──
func scenario10() {
	path := fmt.Sprintf("/documents/%s/file", sofiaID)
	resp, body, err := doRequest("GET", path, nil)
	if err != nil {
		printScenario(10, "File retrieval URL", "GET", path, nil, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}

	// Sofia was ingested as JSON, so s3_key is null → expect 404
	ok := resp.StatusCode == 404
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d, expected 404 (no file for JSON-ingested doc)", resp.StatusCode)
	}
	if ok {
		var fr fileResp
		_ = json.Unmarshal(body, &fr)
		if !strings.Contains(fr.Message, "no file stored") && !strings.Contains(fr.Error, "not_found") {
			ok = false
			reason = fmt.Sprintf("expected 'no file stored' message, got: %s", string(body))
		}
	}
	printScenario(10, "File retrieval URL", "GET", path, nil, resp.StatusCode, body, ok, reason)
}

// ── SCENARIO 11: Delete document ──
func scenario11() {
	path := fmt.Sprintf("/documents/%s", sofiaID)
	resp, body, err := doRequest("DELETE", path, nil)
	if err != nil {
		printScenario(11, "Delete document", "DELETE", path, nil, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	var dr deleteResp
	_ = json.Unmarshal(body, &dr)

	ok := resp.StatusCode == 200 && dr.Deleted
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d deleted=%v", resp.StatusCode, dr.Deleted)
		printScenario(11, "Delete document", "DELETE", path, nil, resp.StatusCode, body, false, reason)
		return
	}

	// Verify: GET file should return 404
	filePath := fmt.Sprintf("/documents/%s/file", sofiaID)
	fileResp, _, fileErr := doRequest("GET", filePath, nil)
	if fileErr != nil || fileResp.StatusCode != 404 {
		status := 0
		if fileResp != nil {
			status = fileResp.StatusCode
		}
		reason = fmt.Sprintf("file endpoint returned %d after delete, expected 404", status)
		printScenario(11, "Delete document", "DELETE", path, nil, resp.StatusCode, body, false, reason)
		return
	}

	// Verify: query with filter_doc_type=candidate should NOT contain SOFIA_ID
	queryBody := map[string]interface{}{
		"query":           "Python Django experience",
		"filter_doc_type": "candidate",
	}
	qResp, qBody, qErr := doRequest("POST", "/query", queryBody)
	if qErr != nil {
		// Non-fatal, just warn
		fmt.Printf("  WARNING: post-delete query failed: %v\n", qErr)
	} else if qResp.StatusCode == 200 {
		var qr queryResp
		_ = json.Unmarshal(qBody, &qr)
		for _, sc := range qr.ScoredChunks {
			if sc.DocID == sofiaID {
				ok = false
				reason = fmt.Sprintf("sofia doc_id=%s still in scored_chunks after delete", sofiaID)
				break
			}
		}
	}

	printScenario(11, "Delete document", "DELETE", path, nil, resp.StatusCode, body, ok, reason)
}

// ── SCENARIO 12: Delete non-existent document ──
func scenario12() {
	path := "/documents/00000000-0000-0000-0000-000000000000"
	resp, body, err := doRequest("DELETE", path, nil)
	if err != nil {
		printScenario(12, "Delete non-existent document", "DELETE", path, nil, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	ok := resp.StatusCode == 404
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d, expected 404", resp.StatusCode)
	}
	printScenario(12, "Delete non-existent document", "DELETE", path, nil, resp.StatusCode, body, ok, reason)
}

// ── SCENARIO 13: Invalid file type rejection ──
func scenario13() {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, _ := writer.CreateFormFile("file", "fake.csv")
	_, _ = part.Write([]byte("col1,col2\nval1,val2"))
	_ = writer.Close()

	req, err := http.NewRequest("POST", baseURL+"/ingest", &buf)
	if err != nil {
		printScenario(13, "Invalid file type rejection", "POST", "/ingest", nil, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		printScenario(13, "Invalid file type rejection", "POST", "/ingest", nil, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	ok := resp.StatusCode == 415
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d, expected 415", resp.StatusCode)
	}
	printScenario(13, "Invalid file type rejection", "POST", "/ingest", "multipart(fake.csv)", resp.StatusCode, body, ok, reason)
}

// ── SCENARIO 14: Health reflects real state ──
func scenario14() {
	resp, body, err := doRequest("GET", "/health", nil)
	if err != nil {
		printScenario(14, "Health reflects real state", "GET", "/health", nil, 0, nil, false, fmt.Sprintf("request error: %v", err))
		return
	}
	var h healthResp
	_ = json.Unmarshal(body, &h)

	ok := resp.StatusCode == 200 && h.Status == "ok" && h.Pinecone == "ok" && h.Neon == "ok" && h.S3 == "ok"
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status=%d health=%+v", resp.StatusCode, h)
	}
	printScenario(14, "Health reflects real state", "GET", "/health", nil, resp.StatusCode, body, ok, reason)
}

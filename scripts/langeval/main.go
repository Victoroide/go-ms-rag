// Lightweight evaluation harness comparing query outputs against a fixed
// representative set. Measures Spanish-language compliance, latency, and
// chunk usage. Designed to be run before and after changes to detect
// regressions without committing anything.
//
//	go run ./scripts/langeval         (runs full eval set)
//	go run ./scripts/langeval -tag baseline   (writes report tagged "baseline")
//	go run ./scripts/langeval -tag after      (writes report tagged "after")
//	go run ./scripts/langeval -compare baseline,after
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const defaultBaseURL = "http://localhost:8081"

type query struct {
	ID       string   // short identifier
	Q        string   // query text
	Lang     string   // user-facing language expected (always "es" in this project)
	Category string   // common | domain | ambiguous | edge | latency-stress | english-bias-risk
	MustHave []string // substrings the answer should contain (loose grounding check)
}

// Representative evaluation set.
//   - Mix of Spanish, English and mixed-language queries — the answer must
//     always be Spanish because the consumer is a Bolivia-facing app.
//   - "english-bias-risk" queries are phrased in English on purpose to
//     stress-test language enforcement.
var evalSet = []query{
	// common
	{ID: "Q01", Q: "¿Qué candidatos tienen experiencia en Python?", Lang: "es", Category: "common"},
	{ID: "Q02", Q: "¿Qué vacantes hay disponibles?", Lang: "es", Category: "common"},
	{ID: "Q03", Q: "Resume las habilidades de los candidatos disponibles.", Lang: "es", Category: "common"},
	// domain-specific
	{ID: "Q04", Q: "¿Cuáles son los requisitos del puesto de Backend Developer en TechBolivia?", Lang: "es", Category: "domain", MustHave: []string{"Python"}},
	{ID: "Q05", Q: "¿Qué candidatos cumplen con los requisitos del puesto Backend Developer de TechBolivia?", Lang: "es", Category: "domain"},
	{ID: "Q06", Q: "Sofia Mamani: ¿qué tecnologías domina y cuántos años de experiencia tiene?", Lang: "es", Category: "domain"},
	// ambiguous / under-specified
	{ID: "Q07", Q: "Cuéntame más.", Lang: "es", Category: "ambiguous"},
	{ID: "Q08", Q: "Mejores opciones", Lang: "es", Category: "ambiguous"},
	// edge / nonsense
	{ID: "Q09", Q: "asdfghjkl 1234", Lang: "es", Category: "edge"},
	{ID: "Q10", Q: "?", Lang: "es", Category: "edge"},
	// latency stress (long query + asks for compare/match → triggers multi-sub-query path)
	{ID: "Q11", Q: "Comparar todos los candidatos con todas las vacantes disponibles, listando para cada vacante los tres candidatos más afines y explicando por qué.", Lang: "es", Category: "latency-stress"},
	// english-bias risk: query is English but answer MUST be Spanish
	{ID: "Q12", Q: "Which candidates have Python and Django experience? Please list them.", Lang: "es", Category: "english-bias-risk"},
	{ID: "Q13", Q: "Tell me about Sofia Mamani's professional background.", Lang: "es", Category: "english-bias-risk"},
	{ID: "Q14", Q: "List the job openings and their required years of experience.", Lang: "es", Category: "english-bias-risk"},
	// mixed-language risk
	{ID: "Q15", Q: "¿Qué candidate tiene más experience en backend?", Lang: "es", Category: "english-bias-risk"},
}

type queryResp struct {
	Answer        string           `json:"answer"`
	Mode          string           `json:"mode"`
	ChunksUsed    int              `json:"chunks_used"`
	ContextChunks int              `json:"context_chunks"`
	ContextTokens int              `json:"context_tokens"`
	EarlyExit     bool             `json:"early_exit"`
	StageTimings  map[string]int64 `json:"stage_timings_ms"`
	TokenUsage    struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"token_usage"`
	ScoredChunks []struct {
		DocID   string  `json:"doc_id"`
		DocType string  `json:"doc_type"`
		Score   float64 `json:"score"`
		Text    string  `json:"text"`
	} `json:"scored_chunks"`
}

type result struct {
	ID            string           `json:"id"`
	Category      string           `json:"category"`
	Query         string           `json:"query"`
	HTTPStatus    int              `json:"http_status"`
	LatencyMS     int64            `json:"latency_ms"`
	ChunksUsed    int              `json:"chunks_used"`
	ContextChunks int              `json:"context_chunks"`
	ContextTokens int              `json:"context_tokens"`
	EarlyExit     bool             `json:"early_exit"`
	Mode          string           `json:"mode"`
	InTokens      int              `json:"in_tokens"`
	OutTokens     int              `json:"out_tokens"`
	IsSpanish     bool             `json:"is_spanish"`
	MixedLang     bool             `json:"mixed_lang"`
	SpanishPct    float64          `json:"spanish_word_pct"`
	Grounded      bool             `json:"grounded"`
	Empty         bool             `json:"empty"`
	Err           string           `json:"err,omitempty"`
	StageTimings  map[string]int64 `json:"stage_timings_ms,omitempty"`
	Answer        string           `json:"answer"`
}

type report struct {
	Tag         string    `json:"tag"`
	When        time.Time `json:"when"`
	BaseURL     string    `json:"base_url"`
	N           int       `json:"n"`
	OK          int       `json:"ok"`
	SpanishOK   int       `json:"spanish_ok"`
	Mixed       int       `json:"mixed"`
	GroundedOK  int       `json:"grounded_ok"`
	P50LatMS    int64     `json:"p50_latency_ms"`
	P95LatMS    int64     `json:"p95_latency_ms"`
	AvgLatMS    int64     `json:"avg_latency_ms"`
	TotalInTok  int       `json:"total_in_tokens"`
	TotalOutTok int       `json:"total_out_tokens"`
	Results     []result  `json:"results"`
}

var (
	// spanishMarkers are diagnostic Spanish function words and connectives
	// that essentially never appear in English prose.
	spanishMarkers = regexp.MustCompile(`(?i)\b(en|de|para|con|los|las|una|que|por|sus|años|también|según|información|disponibles?|candidatos?|vacantes?|empresa|requisitos|habilidades|experiencia|relevante|contexto|según|sobre|hay|cuáles?|qué|quién|donde|cómo|cuándo)\b`)

	// englishMarkers are diagnostic ENGLISH FUNCTION WORDS — words that
	// only occur in English sentences. Proper nouns like "Backend",
	// "Developer", "Engineer", "Python", "Django" are deliberately
	// excluded because they routinely appear inside Spanish prose
	// (job titles, technology names) and would otherwise produce
	// false-positive English drift detections.
	englishMarkers = regexp.MustCompile(`(?i)\b(the|with|and|for|years|skills|experience|provided|context|based|answer|please|note|following|available|listed|requires?|required|is|are|was|were|been|being|to|of|that|this|these|those|which|who|how|when|where|while|though|because|since|but|however|also|therefore|thus|then|here)\b`)

	stripAccents = regexp.MustCompile(`[áéíóúñÁÉÍÓÚÑ¿¡]`)
)

// classifyLanguage looks at the answer and decides if it is purely Spanish,
// purely English, or mixed.  The heuristic is intentionally simple — it
// counts diagnostic word occurrences. We accept a small number of English
// proper nouns (technologies, names) since those are valid in Spanish prose.
func classifyLanguage(answer string) (isSpanish bool, mixed bool, spanishPct float64) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return false, false, 0
	}

	// Quick win: any Spanish accent / inverted punctuation → Spanish lead.
	hasAccent := stripAccents.MatchString(answer)

	esMatches := spanishMarkers.FindAllString(answer, -1)
	enMatches := englishMarkers.FindAllString(answer, -1)

	es := len(esMatches)
	en := len(enMatches)

	if es+en == 0 {
		// short answers like "Sí." or proper nouns only — accept if Spanish-ish.
		if hasAccent {
			return true, false, 1
		}
		// Otherwise: be conservative — count as not-Spanish.
		return false, false, 0
	}
	spanishPct = float64(es) / float64(es+en)
	// Heuristic thresholds:
	//   pure Spanish:   en == 0 or es/(es+en) >= 0.85
	//   pure English:   es == 0 and en >= 3
	//   mixed:          en >= 2 and es >= 2
	switch {
	case en == 0:
		return true, false, 1
	case spanishPct >= 0.85:
		return true, false, spanishPct
	case es == 0 && en >= 3:
		return false, false, 0
	case en >= 2 && es >= 2:
		return false, true, spanishPct
	default:
		// Some English noise but mostly Spanish.
		return spanishPct >= 0.7, en >= 2 && spanishPct < 0.9, spanishPct
	}
}

func runQuery(baseURL, q string) (queryResp, int, time.Duration, error) {
	payload, _ := json.Marshal(map[string]any{"query": q, "mode": "auto"})
	req, err := http.NewRequest("POST", baseURL+"/query", bytes.NewReader(payload))
	if err != nil {
		return queryResp{}, 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	c := &http.Client{Timeout: 120 * time.Second}
	t0 := time.Now()
	resp, err := c.Do(req)
	dur := time.Since(t0)
	if err != nil {
		return queryResp{}, 0, dur, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var qr queryResp
	_ = json.Unmarshal(body, &qr)
	return qr, resp.StatusCode, dur, nil
}

func isGrounded(answer string, must []string) bool {
	if len(must) == 0 {
		return true
	}
	low := strings.ToLower(answer)
	for _, m := range must {
		if !strings.Contains(low, strings.ToLower(m)) {
			return false
		}
	}
	return true
}

// summarize answer so the report stays readable.
func snippet(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	// trim at rune boundary
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

// slowSet are the queries that drove p95 in earlier runs. Used to test
// tail-latency optimizations in isolation without paying the cost of
// the full set on every iteration.
var slowSet = []string{"Q11", "Q12", "Q13", "Q14", "Q15"}

func filterEvalSet(filter string) []query {
	if filter == "" {
		return evalSet
	}
	if filter == "slow" {
		want := make(map[string]bool, len(slowSet))
		for _, id := range slowSet {
			want[id] = true
		}
		out := make([]query, 0, len(slowSet))
		for _, q := range evalSet {
			if want[q.ID] {
				out = append(out, q)
			}
		}
		return out
	}
	ids := make(map[string]bool)
	for _, p := range strings.Split(filter, ",") {
		ids[strings.TrimSpace(p)] = true
	}
	out := make([]query, 0)
	for _, q := range evalSet {
		if ids[q.ID] {
			out = append(out, q)
		}
	}
	return out
}

func runEval(baseURL, tag string, set []query) report {
	rep := report{
		Tag:     tag,
		When:    time.Now().UTC(),
		BaseURL: baseURL,
		N:       len(set),
	}
	durations := make([]int64, 0, len(set))
	for _, q := range set {
		fmt.Printf("[%s] %s  %s\n", tag, q.ID, snippet(q.Q, 70))
		qr, status, dur, err := runQuery(baseURL, q.Q)
		r := result{
			ID:            q.ID,
			Category:      q.Category,
			Query:         q.Q,
			HTTPStatus:    status,
			LatencyMS:     dur.Milliseconds(),
			ChunksUsed:    qr.ChunksUsed,
			ContextChunks: qr.ContextChunks,
			ContextTokens: qr.ContextTokens,
			EarlyExit:     qr.EarlyExit,
			Mode:          qr.Mode,
			InTokens:      qr.TokenUsage.InputTokens,
			OutTokens:     qr.TokenUsage.OutputTokens,
			StageTimings:  qr.StageTimings,
			Answer:        qr.Answer,
		}
		if err != nil {
			r.Err = err.Error()
		}
		r.Empty = strings.TrimSpace(qr.Answer) == ""
		isSp, mixed, pct := classifyLanguage(qr.Answer)
		r.IsSpanish = isSp
		r.MixedLang = mixed
		r.SpanishPct = pct
		r.Grounded = isGrounded(qr.Answer, q.MustHave)

		rep.Results = append(rep.Results, r)
		durations = append(durations, r.LatencyMS)

		if status == 200 && !r.Empty {
			rep.OK++
		}
		if r.IsSpanish {
			rep.SpanishOK++
		}
		if r.MixedLang {
			rep.Mixed++
		}
		if r.Grounded {
			rep.GroundedOK++
		}
		rep.TotalInTok += r.InTokens
		rep.TotalOutTok += r.OutTokens

		fmt.Printf("  -> mode=%s chunks=%d latency=%dms spanish=%v mixed=%v pct=%.2f\n",
			r.Mode, r.ChunksUsed, r.LatencyMS, r.IsSpanish, r.MixedLang, r.SpanishPct)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	rep.P50LatMS = percentile(durations, 0.50)
	rep.P95LatMS = percentile(durations, 0.95)
	var sum int64
	for _, d := range durations {
		sum += d
	}
	if len(durations) > 0 {
		rep.AvgLatMS = sum / int64(len(durations))
	}
	return rep
}

func writeReport(rep report, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	fn := filepath.Join(dir, fmt.Sprintf("eval-%s.json", rep.Tag))
	f, err := os.Create(fn)
	if err != nil {
		return "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		return "", err
	}
	return fn, nil
}

func loadReport(path string) (report, error) {
	var r report
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(b, &r)
	return r, err
}

func printRep(r report) {
	fmt.Printf("\n══ %s ══════════════════════════════════════════════\n", r.Tag)
	fmt.Printf(" N=%d  HTTP-OK=%d  Spanish=%d/%d  Mixed=%d  Grounded=%d\n",
		r.N, r.OK, r.SpanishOK, r.N, r.Mixed, r.GroundedOK)
	fmt.Printf(" Latency avg=%dms  p50=%dms  p95=%dms\n", r.AvgLatMS, r.P50LatMS, r.P95LatMS)
	fmt.Printf(" Tokens in=%d out=%d\n", r.TotalInTok, r.TotalOutTok)
	for _, x := range r.Results {
		marker := "OK "
		switch {
		case !x.IsSpanish:
			marker = "ES!"
		case x.MixedLang:
			marker = "MIX"
		}
		fmt.Printf("  %s [%s] %s  %dms  chunks=%d  -> %s\n",
			marker, x.ID, snippet(x.Query, 50), x.LatencyMS, x.ChunksUsed, snippet(x.Answer, 90))
	}
}

func compare(a, b report) {
	fmt.Printf("\n══ COMPARE %s -> %s ══════════════════════\n", a.Tag, b.Tag)
	fmt.Printf("                          %-12s  %-12s   delta\n", a.Tag, b.Tag)
	fmt.Printf("Spanish answers:           %-12d  %-12d   %+d\n", a.SpanishOK, b.SpanishOK, b.SpanishOK-a.SpanishOK)
	fmt.Printf("Mixed-language answers:    %-12d  %-12d   %+d\n", a.Mixed, b.Mixed, b.Mixed-a.Mixed)
	fmt.Printf("Grounded answers:          %-12d  %-12d   %+d\n", a.GroundedOK, b.GroundedOK, b.GroundedOK-a.GroundedOK)
	fmt.Printf("Avg latency ms:            %-12d  %-12d   %+d\n", a.AvgLatMS, b.AvgLatMS, b.AvgLatMS-a.AvgLatMS)
	fmt.Printf("p50 latency ms:            %-12d  %-12d   %+d\n", a.P50LatMS, b.P50LatMS, b.P50LatMS-a.P50LatMS)
	fmt.Printf("p95 latency ms:            %-12d  %-12d   %+d\n", a.P95LatMS, b.P95LatMS, b.P95LatMS-a.P95LatMS)
	fmt.Printf("Total input tokens:        %-12d  %-12d   %+d\n", a.TotalInTok, b.TotalInTok, b.TotalInTok-a.TotalInTok)
	fmt.Printf("Total output tokens:       %-12d  %-12d   %+d\n", a.TotalOutTok, b.TotalOutTok, b.TotalOutTok-a.TotalOutTok)

	// Per-query comparison
	bm := make(map[string]result, len(b.Results))
	for _, r := range b.Results {
		bm[r.ID] = r
	}
	fmt.Println("\nPer-query language correctness:")
	for _, ar := range a.Results {
		br, ok := bm[ar.ID]
		if !ok {
			continue
		}
		marker := func(r result) string {
			if !r.IsSpanish {
				return "ES!"
			}
			if r.MixedLang {
				return "MIX"
			}
			return "es "
		}
		fmt.Printf("  %s: %s -> %s   lat=%dms->%dms  pct=%.2f->%.2f   %s\n",
			ar.ID, marker(ar), marker(br), ar.LatencyMS, br.LatencyMS, ar.SpanishPct, br.SpanishPct, snippet(ar.Query, 50))
	}
}

// runeFilter strips non-letter glyphs for nicer logging when needed.
func runeFilter(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func main() {
	tag := flag.String("tag", "run", "report tag (e.g. baseline, after)")
	baseURL := flag.String("base", defaultBaseURL, "service base URL")
	out := flag.String("out", "tmp_eval", "output directory")
	cmp := flag.String("compare", "", "comma-separated tag pair to compare (e.g. baseline,after)")
	rescore := flag.String("rescore", "", "comma-separated tag list to re-score with current classifier")
	set := flag.String("set", "", "subset to run (\"slow\" or comma-separated IDs like Q11,Q13). default: full set")
	flag.Parse()

	if *rescore != "" {
		for _, t := range strings.Split(*rescore, ",") {
			t = strings.TrimSpace(t)
			path := filepath.Join(*out, "eval-"+t+".json")
			r, err := loadReport(path)
			if err != nil {
				fmt.Println("rescore load", t, err)
				continue
			}
			r.SpanishOK = 0
			r.Mixed = 0
			for i, x := range r.Results {
				isSp, mx, pct := classifyLanguage(x.Answer)
				r.Results[i].IsSpanish = isSp
				r.Results[i].MixedLang = mx
				r.Results[i].SpanishPct = pct
				if isSp {
					r.SpanishOK++
				}
				if mx {
					r.Mixed++
				}
			}
			if _, err := writeReport(r, *out); err != nil {
				fmt.Println("rescore write", t, err)
				continue
			}
			fmt.Printf("rescored %s: spanish=%d/%d mixed=%d\n", t, r.SpanishOK, r.N, r.Mixed)
		}
		return
	}

	if *cmp != "" {
		parts := strings.Split(*cmp, ",")
		if len(parts) != 2 {
			fmt.Println("compare needs two tags, e.g. -compare baseline,after")
			os.Exit(2)
		}
		a, err := loadReport(filepath.Join(*out, "eval-"+parts[0]+".json"))
		if err != nil {
			fmt.Println("load", parts[0], err)
			os.Exit(2)
		}
		b, err := loadReport(filepath.Join(*out, "eval-"+parts[1]+".json"))
		if err != nil {
			fmt.Println("load", parts[1], err)
			os.Exit(2)
		}
		compare(a, b)
		return
	}

	rep := runEval(*baseURL, *tag, filterEvalSet(*set))
	fn, err := writeReport(rep, *out)
	if err != nil {
		fmt.Println("write", err)
		os.Exit(2)
	}
	printRep(rep)
	fmt.Printf("\nReport written to %s\n", fn)
	_ = runeFilter // silence unused if no consumer
}

package extractor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	childHooks = map[FileType]func() (string, error){
		"sleep": func() (string, error) { time.Sleep(time.Minute); return "late", nil },
		"env":   func() (string, error) { return strings.Join(os.Environ(), "\n"), nil },
		"huge":  func() (string, error) { return strings.Repeat("x", 9<<20), nil },
	}
	if len(os.Args) == 2 && os.Args[1] == ChildArg && os.Getenv(ChildEnv) == "1" {
		os.Exit(RunChild(os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

func sandboxPDF(pages string) []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 " + pages + " >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		"<< /Length 48 >>\nstream\nBT /F1 12 Tf (Archive operating hours.) Tj ET\nendstream",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, object := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}

func uploadSandbox(t *testing.T, timeout time.Duration) *Sandbox {
	t.Helper()
	sb, err := NewSandbox(SandboxConfig{Timeout: timeout, MemoryLimitMB: 512})
	if err != nil {
		t.Fatal(err)
	}
	return sb
}

func TestSandboxExtractsPDF(t *testing.T) {
	text, err := uploadSandbox(t, 5*time.Second).Extract(t.Context(), FileTypePDF, sandboxPDF(""))
	if err != nil || !strings.Contains(text, "Archive operating hours.") {
		t.Fatalf("PDF extraction: text=%q error=%v", text, err)
	}
}

func TestSandboxSurvivesParserStackOverflow(t *testing.T) {
	sb := uploadSandbox(t, 15*time.Second)
	data := sandboxPDF("/Bomb " + strings.Repeat("[", 6_000_000) + strings.Repeat("]", 6_000_000))
	start := time.Now()
	if _, err := sb.Extract(t.Context(), FileTypePDF, data); !errors.Is(err, ErrExtractionFailed) {
		t.Fatalf("malformed PDF: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("malformed PDF took %s", elapsed)
	}
	if _, err := sb.Extract(t.Context(), FileTypePDF, sandboxPDF("")); err != nil {
		t.Fatalf("extraction after child crash: %v", err)
	}
}

func TestSandboxEnforcesTimeout(t *testing.T) {
	start := time.Now()
	_, err := uploadSandbox(t, 200*time.Millisecond).Extract(t.Context(), "sleep", nil)
	if !errors.Is(err, ErrExtractionTimeout) {
		t.Fatalf("timeout: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("child was not terminated promptly: %s", elapsed)
	}
}

func TestSandboxScrubsEnvironment(t *testing.T) {
	t.Setenv("SERVICE_TOKEN", "x")
	t.Setenv("AWS_ACCESS_KEY_ID", "x")
	t.Setenv("DATABASE_URL", "x")
	text, err := uploadSandbox(t, 5*time.Second).Extract(t.Context(), "env", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range strings.Split(text, "\n") {
		if strings.HasPrefix(entry, "SERVICE_TOKEN=") || strings.HasPrefix(entry, "AWS_") || strings.HasPrefix(entry, "DATABASE_URL=") {
			t.Fatalf("inherited environment entry: %q", entry)
		}
	}
	for _, entry := range []string{"QUARRY_EXTRACT_CHILD=1", "GOMAXPROCS=1", "GOMEMLIMIT=512MiB"} {
		if !strings.Contains(text, entry) {
			t.Fatalf("missing child setting %q in %q", entry, text)
		}
	}
}

func TestSandboxBoundsOutput(t *testing.T) {
	_, err := uploadSandbox(t, 5*time.Second).Extract(t.Context(), "huge", nil)
	if !errors.Is(err, ErrExtractionFailed) {
		t.Fatalf("oversized output: %v", err)
	}
}

func TestLimiterRejectsWhenBusy(t *testing.T) {
	limiter := NewLimiter(1, 10*time.Millisecond)
	release, err := limiter.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, acquireErr := limiter.Acquire(t.Context()); !errors.Is(acquireErr, ErrBusy) {
		t.Fatalf("occupied limiter: %v", acquireErr)
	}
	release()
	release()
	release, err = limiter.Acquire(t.Context())
	if err != nil {
		t.Fatalf("released limiter: %v", err)
	}
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := limiter.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition: %v", err)
	}
}

func TestRunChildRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{
		strings.Repeat("x", 1025) + "\n",
		"{bad}\n",
		`{"type":"unknown","timeout_ms":1000,"max_stack":67108864}` + "\n",
		`{"type":"pdf","timeout_ms":0,"max_stack":67108864}` + "\n",
	} {
		var out bytes.Buffer
		if code := RunChild(strings.NewReader(input), &out); code == 0 && !strings.Contains(out.String(), `"error"`) {
			t.Fatalf("invalid input accepted: code=%d output=%s", code, &out)
		}
	}
}

package extractor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime/debug"
	"strconv"
	"time"
)

// ChildArg selects the extraction entry point in the service binary.
const ChildArg = "__quarry-extract"

// ChildEnv enables the extraction entry point when paired with ChildArg.
const ChildEnv = "QUARRY_EXTRACT_CHILD"

const childMaxStack = 64 << 20

// Extraction failures distinguish a timeout, parser failure and unavailable executable.
var (
	ErrExtractionTimeout     = errors.New("extractor: extraction timed out")
	ErrExtractionFailed      = errors.New("extractor: document could not be parsed")
	ErrExtractionUnavailable = errors.New("extractor: extraction is unavailable")
)

// SandboxConfig sets the child executable, wall-clock timeout and soft memory limit.
type SandboxConfig struct {
	Executable    string
	Timeout       time.Duration
	MemoryLimitMB int
}

// Sandbox extracts documents in a separate process with bounded input and output.
type Sandbox struct {
	executable    string
	timeout       time.Duration
	memoryLimitMB int
}

// NewSandbox defaults to the current executable and checks its availability.
func NewSandbox(cfg SandboxConfig) (*Sandbox, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MemoryLimitMB == 0 {
		cfg.MemoryLimitMB = 512
	}
	if cfg.Timeout < time.Millisecond || cfg.Timeout > 10*time.Minute || cfg.MemoryLimitMB < 64 || cfg.MemoryLimitMB > 8192 {
		return nil, fmt.Errorf("%w: invalid extraction limits", ErrExtractionUnavailable)
	}
	if cfg.Executable == "" {
		var err error
		cfg.Executable, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrExtractionUnavailable, err)
		}
	}
	path, err := exec.LookPath(cfg.Executable)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExtractionUnavailable, err)
	}
	return &Sandbox{executable: path, timeout: cfg.Timeout, memoryLimitMB: cfg.MemoryLimitMB}, nil
}

type childHeader struct {
	Type      FileType `json:"type"`
	TimeoutMS int64    `json:"timeout_ms"`
	MaxStack  int      `json:"max_stack"`
}

type childResponse struct {
	Text  *string `json:"text,omitempty"`
	Error string  `json:"error,omitempty"`
}

// Extract runs one child process, terminating it on cancellation or exceeded limits.
func (s *Sandbox) Extract(ctx context.Context, ft FileType, data []byte) (string, error) {
	if len(data) > maxSourceBytes {
		return "", fmt.Errorf("extractor: source exceeds maximum size of %d bytes", maxSourceBytes)
	}
	childCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	header, err := json.Marshal(childHeader{Type: ft, TimeoutMS: s.timeout.Milliseconds(), MaxStack: childMaxStack})
	if err != nil {
		return "", fmt.Errorf("%w: header: %w", ErrExtractionFailed, err)
	}
	cmd := exec.CommandContext(childCtx, s.executable, ChildArg) // #nosec G204 -- executable is resolved at construction, arguments are fixed.
	cmd.Env = []string{ChildEnv + "=1", "GOMAXPROCS=1", "GOMEMLIMIT=" + strconv.Itoa(s.memoryLimitMB) + "MiB"}
	cmd.Stdin = io.MultiReader(bytes.NewReader(append(header, '\n')), bytes.NewReader(data))
	stdout := &cappedBuffer{limit: 8 << 20, cancel: cancel}
	stderr := &cappedBuffer{limit: 64 << 10, cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	if err != nil || stdout.exceeded || stderr.exceeded {
		slog.WarnContext(ctx, "document extraction child failed", "error", err, "stderr", stderr.buffer.String())
		if stdout.exceeded || stderr.exceeded {
			return "", ErrExtractionFailed
		}
		if errors.Is(childCtx.Err(), context.DeadlineExceeded) {
			return "", ErrExtractionTimeout
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var pathErr *os.PathError
		var execErr *exec.Error
		if errors.As(err, &pathErr) || errors.As(err, &execErr) {
			return "", ErrExtractionUnavailable
		}
		return "", ErrExtractionFailed
	}
	var response childResponse
	if err := json.Unmarshal(stdout.buffer.Bytes(), &response); err != nil {
		return "", ErrExtractionFailed
	}
	if response.Error != "" {
		return "", errors.New(response.Error)
	}
	if response.Text == nil {
		return "", ErrExtractionFailed
	}
	return *response.Text, nil
}

type cappedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	cancel   context.CancelFunc
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		n, _ := b.buffer.Write(p[:b.limit-b.buffer.Len()])
		b.exceeded = true
		b.cancel()
		return n, ErrExtractionFailed
	}
	return b.buffer.Write(p)
}

var childHooks map[FileType]func() (string, error)

// RunChild reads one extraction request and writes one JSON response.
func RunChild(stdin io.Reader, stdout io.Writer) int {
	reader := bufio.NewReaderSize(stdin, 1024)
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) > 1024 {
		return writeChildResponse(stdout, "", errors.New("extractor: invalid extraction header"))
	}
	var header childHeader
	if decodeErr := json.Unmarshal(line, &header); decodeErr != nil || header.TimeoutMS < 1 || header.TimeoutMS > (10*time.Minute).Milliseconds() || header.MaxStack < 1 || header.MaxStack > childMaxStack {
		return writeChildResponse(stdout, "", errors.New("extractor: invalid extraction header"))
	}
	debug.SetMaxStack(header.MaxStack)
	// Prefer terminating the parser if the kernel must reclaim memory.
	_ = os.WriteFile("/proc/self/oom_score_adj", []byte("1000"), 0o600)
	hook := childHooks[header.Type]
	if hook == nil && header.Type != FileTypePDF && header.Type != FileTypeDOCX && header.Type != FileTypeDOC {
		return writeChildResponse(stdout, "", errors.New("extractor: unsupported file type"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(header.TimeoutMS)*time.Millisecond)
	defer cancel()
	data, err := readSource(ctx, reader)
	if err != nil {
		return writeChildResponse(stdout, "", err)
	}
	var text string
	if hook != nil {
		text, err = hook()
	} else {
		text, err = New(header.Type).Extract(ctx, bytes.NewReader(data))
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return 1
	}
	return writeChildResponse(stdout, text, err)
}

func writeChildResponse(stdout io.Writer, text string, err error) int {
	response := childResponse{Text: &text}
	if err != nil {
		response.Text, response.Error = nil, err.Error()
	}
	if encodeErr := json.NewEncoder(stdout).Encode(response); encodeErr != nil {
		return 1
	}
	return 0
}

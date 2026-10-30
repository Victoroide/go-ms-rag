package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/api/handler"
)

func TestServeWaitsForActiveRequests(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { close(started); <-release; w.WriteHeader(204) })}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- serve(ctx, srv, listener, handler.NewProbes(nil, nil, nil, time.Minute), 0) }()
	requestDone := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String(), http.NoBody)
		if err != nil {
			requestDone <- err
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			err = resp.Body.Close()
		}
		requestDone <- err
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		t.Fatalf("shutdown returned before active request completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestServeSecondSignalSkipsDrain(t *testing.T) {
	if os.Getenv("QUARRY_TEST_DRAIN_CHILD") == "1" {
		listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p := handler.NewProbes(nil, nil, nil, time.Minute)
		srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: handler.LivenessHandler()}
		ctx, stop := signal.NotifyContext(t.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		go func() {
			for !p.Draining() {
				time.Sleep(time.Millisecond)
			}
			fmt.Println("draining")
		}()
		fmt.Println("ready")
		if err := serve(ctx, srv, listener, p, time.Minute); err != nil {
			t.Fatal(err)
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestServeSecondSignalSkipsDrain$") // #nosec G204 -- executable is the current test binary from os.Executable.
	cmd.Env = append(os.Environ(), "QUARRY_TEST_DRAIN_CHILD=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	for _, want := range []string{"ready", "draining"} {
		if !scanner.Scan() || scanner.Text() != want {
			cancel()
			waitErr := cmd.Wait()
			t.Fatalf("child state = %q, want %q: %v, %v, %s", scanner.Text(), want, scanner.Err(), waitErr, stderr.String())
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			cancel()
			waitErr := cmd.Wait()
			t.Fatalf("signal child: %v; wait: %v", err, waitErr)
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("second signal did not finish shutdown: %v, %s", err, stderr.String())
	}
}

func TestServeDrainsBeforeShutdown(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := handler.NewProbes(nil, nil, nil, time.Minute)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", handler.LivenessHandler())
	mux.HandleFunc("GET /readyz", handler.ReadinessHandler(p))
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: mux}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- serve(ctx, srv, listener, p, 200*time.Millisecond) }()
	cancel()
	deadline := time.After(time.Second)
	for !p.Draining() {
		select {
		case <-deadline:
			t.Fatal("shutdown did not mark readiness draining")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	for _, tt := range []struct {
		path string
		code int
	}{{"/livez", http.StatusOK}, {"/readyz", http.StatusServiceUnavailable}} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String()+tt.path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tt.code {
			t.Errorf("%s during drain = %d", tt.path, resp.StatusCode)
		}
	}
	select {
	case err := <-result:
		t.Fatalf("server stopped before drain elapsed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

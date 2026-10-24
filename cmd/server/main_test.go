package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
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
	go func() { result <- serve(ctx, srv, listener) }()
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

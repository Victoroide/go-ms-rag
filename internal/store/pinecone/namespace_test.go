package pinecone

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/pinecone-io/go-pinecone/v5/pinecone"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type namespaceListener struct {
	net.Listener
	accepted atomic.Int32
}

func (l *namespaceListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return conn, err
}

func TestDerivedNamespaceConnectionIsNotClosed(t *testing.T) {
	for _, operation := range []struct {
		name string
		run  func(*testing.T, *Client) error
	}{
		{"upsert", func(t *testing.T, c *Client) error { return c.Upsert(t.Context(), "legacy", testChunks(1, 1024, 10)) }},
		{"query", func(t *testing.T, c *Client) error {
			_, err := c.Query(t.Context(), "legacy", []float32{1}, 1, "")
			return err
		}},
		{"delete", func(t *testing.T, c *Client) error { return c.Delete(t.Context(), "legacy", []string{"id"}) }},
		{"delete namespace", func(t *testing.T, c *Client) error { return c.DeleteAllVectorsInNamespace(t.Context(), "legacy") }},
		{"query error", func(t *testing.T, c *Client) error {
			_, err := c.Query(t.Context(), "legacy", []float32{1}, 1, "")
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected invalid argument, got %v", err)
			}
			return nil
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			root := &fakeIndexConn{}
			derived := &fakeIndexConn{}
			if operation.name == "query error" {
				derived.query = func(context.Context, *pinecone.QueryByVectorValuesRequest) (*pinecone.QueryVectorsResponse, error) {
					return nil, status.Error(codes.InvalidArgument, "invalid")
				}
			}
			client := testClient(root)
			client.dial = func(namespace string) (indexConn, error) {
				if namespace != "legacy" {
					t.Fatalf("unexpected namespace %q", namespace)
				}
				return derived, nil
			}
			if err := operation.run(t, client); err != nil {
				t.Fatal(err)
			}
			if derived.closed != 0 || root.closed != 0 {
				t.Fatalf("namespace operation closed shared connection: derived=%d root=%d", derived.closed, root.closed)
			}
			if _, err := client.Query(t.Context(), "", []float32{1}, 1, ""); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if root.closed != 1 || derived.closed != 0 {
				t.Fatalf("shutdown closes: root=%d derived=%d", root.closed, derived.closed)
			}
		})
	}
}

func TestSDKNamespaceOperationsPreserveSharedTransport(t *testing.T) {
	tcpListener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &namespaceListener{Listener: tcpListener}
	server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		md, _ := metadata.FromIncomingContext(stream.Context())
		if got := md.Get("x-pinecone-api-version"); len(got) != 1 || got[0] != "2025-10" {
			return status.Error(codes.FailedPrecondition, "missing API version")
		}
		return status.Error(codes.InvalidArgument, "invalid vector")
	}))
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve(listener) }()
	pc, err := pinecone.NewClient(pinecone.NewClientParams{ApiKey: "local-test"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := pc.Index(pinecone.NewIndexConnParams{Host: "http://" + listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	if root.Namespace() != "__default__" {
		t.Fatalf("default namespace=%q", root.Namespace())
	}
	client := newClient(pc, &config.Config{
		PineconeTimeout: 2 * time.Second, PineconeMaxAttempts: 1,
		PineconeUpsertBatchSize: 100, EmbeddingDimensions: 1,
	}, root)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close shared connection: %v", err)
		}
	})
	for _, namespace := range []string{"legacy", "", "another", ""} {
		_, err := client.Query(t.Context(), namespace, []float32{1}, 1, "")
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("namespace %q lost transport status: %v", namespace, err)
		}
	}
	if got := listener.accepted.Load(); got != 1 {
		t.Fatalf("namespace operations opened %d connections, want 1", got)
	}
}

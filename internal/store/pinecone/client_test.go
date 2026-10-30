package pinecone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/pinecone-io/go-pinecone/v5/pinecone"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type fakeIndexConn struct {
	upsert       func(context.Context, []*pinecone.Vector) (uint32, error)
	query        func(context.Context, *pinecone.QueryByVectorValuesRequest) (*pinecone.QueryVectorsResponse, error)
	filterDelete func(context.Context, *structpb.Struct) error
	deleteIDs    func(context.Context, []string) error
	deleteAll    func(context.Context) error
	closed       int
}

func (f *fakeIndexConn) UpsertVectors(ctx context.Context, vectors []*pinecone.Vector) (uint32, error) {
	if f.upsert != nil {
		return f.upsert(ctx, vectors)
	}
	if len(vectors) > 1000 {
		return 0, errors.New("too many vectors")
	}
	return uint32(len(vectors)), nil // #nosec G115 -- The batch length is bounded above.
}
func (f *fakeIndexConn) QueryByVectorValues(ctx context.Context, req *pinecone.QueryByVectorValuesRequest) (*pinecone.QueryVectorsResponse, error) {
	if f.query != nil {
		return f.query(ctx, req)
	}
	return &pinecone.QueryVectorsResponse{}, nil
}
func (f *fakeIndexConn) DeleteVectorsByFilter(ctx context.Context, filter *structpb.Struct) error {
	if f.filterDelete != nil {
		return f.filterDelete(ctx, filter)
	}
	return nil
}

//revive:disable-next-line:var-naming SDK method name.
func (f *fakeIndexConn) DeleteVectorsById(ctx context.Context, ids []string) error {
	if f.deleteIDs != nil {
		return f.deleteIDs(ctx, ids)
	}
	return nil
}
func (f *fakeIndexConn) DeleteAllVectorsInNamespace(ctx context.Context) error {
	if f.deleteAll != nil {
		return f.deleteAll(ctx)
	}
	return nil
}
func (f *fakeIndexConn) Close() error { f.closed++; return nil }

func testClient(conn *fakeIndexConn) *Client {
	return &Client{defaultConn: conn, batchSize: 100, timeout: time.Second, maxAttempts: 5, dims: 1024}
}

func testChunks(count, dims, textBytes int) []domain.Chunk {
	chunks := make([]domain.Chunk, count)
	for i := range chunks {
		chunks[i] = domain.Chunk{ID: domain.ChunkID(fmt.Sprintf("chunk-%d", i)), DocumentID: "doc", Index: i, Text: strings.Repeat("x", textBytes), Embedding: make([]float32, dims)}
	}
	return chunks
}

func TestUpsertSplitsByCountAndBytes(t *testing.T) {
	for _, batchSize := range []int{100, 1000} {
		t.Run(fmt.Sprint(batchSize), func(t *testing.T) {
			seen := make(map[string]bool)
			calls := 0
			conn := &fakeIndexConn{upsert: func(_ context.Context, vectors []*pinecone.Vector) (uint32, error) {
				calls++
				if len(vectors) == 0 || len(vectors) > batchSize {
					t.Fatalf("batch has %d vectors, limit %d", len(vectors), batchSize)
				}
				size := 0
				for _, v := range vectors {
					size += 4*len(*v.Values) + len(v.Id) + proto.Size(v.Metadata) + 32
					if seen[v.Id] {
						t.Fatalf("duplicate vector %s", v.Id)
					}
					seen[v.Id] = true
				}
				if size > 1572864 {
					t.Fatalf("batch estimate is %d bytes", size)
				}
				if len(vectors) > 1000 {
					return 0, errors.New("too many vectors")
				}
				return uint32(len(vectors)), nil // #nosec G115 -- The batch length is bounded above.
			}}
			client := testClient(conn)
			client.batchSize = batchSize
			if err := client.Upsert(t.Context(), "", testChunks(950, 1024, 2400)); err != nil {
				t.Fatal(err)
			}
			if len(seen) != 950 || calls < 2 {
				t.Fatalf("received %d vectors in %d batches", len(seen), calls)
			}
		})
	}
}

func TestUpsertRetriesTransientCodes(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.ResourceExhausted, codes.Aborted, codes.Internal, codes.DeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				conn := &fakeIndexConn{upsert: func(ctx context.Context, _ []*pinecone.Vector) (uint32, error) {
					if _, ok := ctx.Deadline(); !ok {
						t.Fatal("missing per-attempt deadline")
					}
					calls++
					if calls < 3 {
						return 0, status.Error(code, "temporary")
					}
					return 1, nil
				}}
				if err := testClient(conn).Upsert(t.Context(), "", testChunks(1, 1024, 10)); err != nil {
					t.Fatal(err)
				}
				if calls != 3 {
					t.Fatalf("got %d attempts", calls)
				}
			})
		})
	}
}

func TestUpsertDoesNotRetryInvalidArgument(t *testing.T) {
	calls := 0
	conn := &fakeIndexConn{upsert: func(context.Context, []*pinecone.Vector) (uint32, error) {
		calls++
		return 0, status.Error(codes.InvalidArgument, "invalid")
	}}
	err := testClient(conn).Upsert(t.Context(), "", testChunks(1, 1024, 10))
	if status.Code(err) != codes.InvalidArgument || calls != 1 {
		t.Fatalf("attempts=%d error=%v", calls, err)
	}
}

func TestRetriesStopWhenContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		client := testClient(&fakeIndexConn{upsert: func(context.Context, []*pinecone.Vector) (uint32, error) {
			calls++
			cancel()
			return 0, status.Error(codes.Unavailable, "temporary")
		}})
		if err := client.Upsert(ctx, "", testChunks(1, 1024, 10)); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation: %v", err)
		}
		if calls != 1 {
			t.Fatalf("got %d attempts after cancellation", calls)
		}
		if err := client.Upsert(ctx, "", testChunks(1, 1024, 10)); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation: %v", err)
		}
		if calls != 1 {
			t.Fatal("called transport with canceled context")
		}
	})
}

func TestAttemptTimeoutAndLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		client := testClient(&fakeIndexConn{})
		err := client.do(t.Context(), 3, func(ctx context.Context) error { calls++; <-ctx.Done(); return ctx.Err() })
		if !errors.Is(err, context.DeadlineExceeded) || calls != 3 {
			t.Fatalf("attempts=%d error=%v", calls, err)
		}
	})
}

func TestRecordMetadataAbove40KBIsRejected(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", 40960), strings.Repeat("\x01", 7000)} {
		calls := 0
		client := testClient(&fakeIndexConn{upsert: func(context.Context, []*pinecone.Vector) (uint32, error) { calls++; return 1, nil }})
		chunks := testChunks(2, 1024, 10)
		chunks[1].Text = text
		err := client.Upsert(t.Context(), "", chunks)
		if !errors.Is(err, ErrMetadataTooLarge) || calls != 0 {
			t.Fatalf("attempts=%d error=%v", calls, err)
		}
	}
}

func TestRecordMetadataAt40KBIsAccepted(t *testing.T) {
	md, err := json.Marshal(map[string]any{"doc_id": "doc", "chunk_index": float64(0), "text": "", "token_count": float64(0)})
	if err != nil {
		t.Fatal(err)
	}
	chunks := testChunks(1, 1024, 40960-len(md))
	if err := testClient(&fakeIndexConn{}).Upsert(t.Context(), "", chunks); err != nil {
		t.Fatal(err)
	}
}

func TestValidateIndexRejectsDimensionMismatch(t *testing.T) {
	idx := &pinecone.Index{Dimension: new(int32(512)), Metric: pinecone.Cosine, Status: &pinecone.IndexStatus{Ready: true}}
	if err := validateIndex(idx, &config.Config{EmbeddingDimensions: 1024}); err == nil {
		t.Fatal("accepted incompatible dimensions")
	}
}

func TestValidateIndexRejectsMissingDimension(t *testing.T) {
	idx := &pinecone.Index{Metric: pinecone.Cosine, Status: &pinecone.IndexStatus{Ready: true}}
	if err := validateIndex(idx, &config.Config{EmbeddingDimensions: 1024}); err == nil {
		t.Fatal("accepted missing dimension")
	}
}

func TestUpsertPreservesVectorValues(t *testing.T) {
	chunks := testChunks(2, 2, 10)
	chunks[0].Embedding = []float32{0.25, -0.5}
	chunks[1].Embedding = []float32{0.75, 1}
	calls := 0
	conn := &fakeIndexConn{upsert: func(_ context.Context, vectors []*pinecone.Vector) (uint32, error) {
		calls++
		if len(vectors) != 2 {
			t.Fatalf("got %d vectors", len(vectors))
		}
		for i, want := range [][]float32{{0.25, -0.5}, {0.75, 1}} {
			if vectors[i].Values == nil || len(*vectors[i].Values) != 2 {
				t.Fatalf("missing values for vector %d", i)
			}
			for j, value := range want {
				if (*vectors[i].Values)[j] != value {
					t.Fatalf("vector %d value %d: got %v, want %v", i, j, (*vectors[i].Values)[j], value)
				}
			}
		}
		return 2, nil
	}}
	if err := testClient(conn).Upsert(t.Context(), "", chunks); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("upsert called %d times", calls)
	}
}
func TestValidateIndexRejectsNonCosine(t *testing.T) {
	idx := &pinecone.Index{Dimension: new(int32(1024)), Metric: pinecone.Dotproduct, Status: &pinecone.IndexStatus{Ready: true}}
	if err := validateIndex(idx, &config.Config{EmbeddingDimensions: 1024}); err == nil {
		t.Fatal("accepted incompatible metric")
	}
}
func TestValidateIndexReadiness(t *testing.T) {
	idx := &pinecone.Index{Dimension: new(int32(1024)), Metric: pinecone.Cosine}
	cfg := &config.Config{EmbeddingDimensions: 1024}
	if err := validateIndex(idx, cfg); err == nil {
		t.Fatal("accepted missing status")
	}
	idx.Status = &pinecone.IndexStatus{}
	if err := validateIndex(idx, cfg); err == nil {
		t.Fatal("accepted unready index")
	}
	idx.Status.Ready = true
	if err := validateIndex(idx, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestClientReusesDefaultConnection(t *testing.T) {
	conn := &fakeIndexConn{}
	client := testClient(conn)
	dials := 0
	client.dial = func(string) (indexConn, error) { dials++; return &fakeIndexConn{}, nil }
	for range 2 {
		if err := client.Upsert(t.Context(), "", testChunks(1, 1024, 10)); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Query(t.Context(), "", []float32{1}, 1, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := client.QueryAllNamespaces(t.Context(), []float32{1}, 1, ""); err != nil {
			t.Fatal(err)
		}
		if err := client.Delete(t.Context(), "", []string{"id"}); err != nil {
			t.Fatal(err)
		}
	}
	if dials != 0 || conn.closed != 0 {
		t.Fatalf("dials=%d closes=%d", dials, conn.closed)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if conn.closed != 1 {
		t.Fatalf("connection closed %d times", conn.closed)
	}
}

func TestLegacyNamespaceConnectionRemainsOpen(t *testing.T) {
	client := testClient(&fakeIndexConn{})
	var conns []*fakeIndexConn
	client.dial = func(namespace string) (indexConn, error) {
		if namespace != "legacy" {
			t.Fatalf("namespace=%q", namespace)
		}
		conn := &fakeIndexConn{deleteAll: func(context.Context) error { return status.Error(codes.NotFound, "missing") }}
		conns = append(conns, conn)
		return conn, nil
	}
	for range 2 {
		if err := client.DeleteAllVectorsInNamespace(t.Context(), "legacy"); err != nil {
			t.Fatal(err)
		}
	}
	if len(conns) != 2 {
		t.Fatalf("dialed %d connections", len(conns))
	}
	for _, conn := range conns {
		if conn.closed != 0 {
			t.Fatalf("closed %d times", conn.closed)
		}
	}
}

func TestLegacyConnectionRemainsOpenOnError(t *testing.T) {
	conn := &fakeIndexConn{query: func(context.Context, *pinecone.QueryByVectorValuesRequest) (*pinecone.QueryVectorsResponse, error) {
		return nil, status.Error(codes.InvalidArgument, "bad")
	}}
	client := testClient(&fakeIndexConn{})
	client.dial = func(string) (indexConn, error) { return conn, nil }
	if _, err := client.Query(t.Context(), "legacy", []float32{1}, 1, ""); err == nil {
		t.Fatal("expected query failure")
	}
	if conn.closed != 0 {
		t.Fatalf("closed %d times", conn.closed)
	}
}

func TestQueryRetriesAtMostThreeTimes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		client := testClient(&fakeIndexConn{query: func(_ context.Context, req *pinecone.QueryByVectorValuesRequest) (*pinecone.QueryVectorsResponse, error) {
			calls++
			if !req.IncludeMetadata || req.MetadataFilter.GetFields()["doc_type"].GetStringValue() != "report" {
				t.Fatal("query options lost")
			}
			return nil, status.Error(codes.Unavailable, "temporary")
		}})
		if _, err := client.Query(t.Context(), "", []float32{1}, 1, "report"); status.Code(err) != codes.Unavailable || calls != 3 {
			t.Fatalf("attempts=%d error=%v", calls, err)
		}
	})
}

func TestFilterDeleteFallsBackToIDs(t *testing.T) {
	for _, code := range []codes.Code{codes.InvalidArgument, codes.Unimplemented, codes.FailedPrecondition} {
		t.Run(code.String(), func(t *testing.T) {
			queries, deletes := 0, 0
			conn := &fakeIndexConn{filterDelete: func(context.Context, *structpb.Struct) error { return status.Error(code, "unsupported") }}
			conn.query = func(_ context.Context, req *pinecone.QueryByVectorValuesRequest) (*pinecone.QueryVectorsResponse, error) {
				queries++
				if len(req.Vector) != 1024 || req.Vector[0] != 1 || req.TopK != 1000 || req.IncludeMetadata || req.IncludeValues {
					t.Fatalf("unexpected query: %+v", req)
				}
				for _, v := range req.Vector[1:] {
					if v != 0 {
						t.Fatal("query is not e1")
					}
				}
				if req.MetadataFilter.GetFields()["doc_id"].GetStructValue().GetFields()["$eq"].GetStringValue() != "doc" {
					t.Fatal("missing document filter")
				}
				res := &pinecone.QueryVectorsResponse{}
				if queries == 1 {
					for i := range 1000 {
						res.Matches = append(res.Matches, &pinecone.ScoredVector{Vector: &pinecone.Vector{Id: fmt.Sprint(i)}})
					}
				}
				return res, nil
			}
			conn.deleteIDs = func(_ context.Context, ids []string) error {
				deletes++
				if len(ids) != 1000 {
					t.Fatalf("delete contains %d ids", len(ids))
				}
				return nil
			}
			if err := testClient(conn).DeleteByDocID(t.Context(), "doc"); err != nil {
				t.Fatal(err)
			}
			if queries != 2 || deletes != 1 {
				t.Fatalf("queries=%d deletes=%d", queries, deletes)
			}
		})
	}
}

func TestFilterDeleteFallbackIsBounded(t *testing.T) {
	queries, deletes := 0, 0
	conn := &fakeIndexConn{filterDelete: func(context.Context, *structpb.Struct) error { return status.Error(codes.Unimplemented, "unsupported") }, query: func(context.Context, *pinecone.QueryByVectorValuesRequest) (*pinecone.QueryVectorsResponse, error) {
		queries++
		res := &pinecone.QueryVectorsResponse{}
		for i := range 1000 {
			res.Matches = append(res.Matches, &pinecone.ScoredVector{Vector: &pinecone.Vector{Id: fmt.Sprintf("%d-%d", queries, i)}})
		}
		return res, nil
	}}
	conn.deleteIDs = func(_ context.Context, ids []string) error {
		deletes++
		if len(ids) != 1000 {
			t.Fatalf("delete contains %d ids", len(ids))
		}
		return nil
	}
	if err := testClient(conn).DeleteByDocID(t.Context(), "doc"); err == nil || queries != 10 || deletes != 10 {
		t.Fatalf("queries=%d deletes=%d error=%v", queries, deletes, err)
	}
}

func TestFilterDeleteFallbackToleratesIndexLag(t *testing.T) {
	for _, count := range []int{2, 1000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			queries, deletes := 0, 0
			deleted := make(map[string]bool)
			conn := &fakeIndexConn{filterDelete: func(context.Context, *structpb.Struct) error { return status.Error(codes.Unimplemented, "unsupported") }}
			conn.query = func(context.Context, *pinecone.QueryByVectorValuesRequest) (*pinecone.QueryVectorsResponse, error) {
				queries++
				res := &pinecone.QueryVectorsResponse{}
				for i := range count {
					res.Matches = append(res.Matches, &pinecone.ScoredVector{Vector: &pinecone.Vector{Id: fmt.Sprint(i)}})
				}
				return res, nil
			}
			conn.deleteIDs = func(_ context.Context, ids []string) error {
				deletes++
				for _, id := range ids {
					if deleted[id] {
						t.Errorf("identifier %s deleted more than once", id)
					}
					deleted[id] = true
				}
				return nil
			}
			if err := testClient(conn).DeleteByDocID(t.Context(), "doc"); err != nil {
				t.Fatal(err)
			}
			wantQueries := 1
			if count == 1000 {
				wantQueries = 2
			}
			if queries != wantQueries || deletes != 1 || len(deleted) != count {
				t.Fatalf("queries=%d deletes=%d identifiers=%d", queries, deletes, len(deleted))
			}
		})
	}
}

func TestFilterDeleteFallbackDeletesOnlyNewIDs(t *testing.T) {
	queries, deletes := 0, 0
	deleted := make(map[string]bool)
	conn := &fakeIndexConn{filterDelete: func(context.Context, *structpb.Struct) error { return status.Error(codes.Unimplemented, "unsupported") }}
	conn.query = func(context.Context, *pinecone.QueryByVectorValuesRequest) (*pinecone.QueryVectorsResponse, error) {
		queries++
		start := 0
		if queries > 1 {
			start = 500
		}
		res := &pinecone.QueryVectorsResponse{}
		for i := start; i < start+1000; i++ {
			res.Matches = append(res.Matches, &pinecone.ScoredVector{Vector: &pinecone.Vector{Id: fmt.Sprint(i)}})
		}
		return res, nil
	}
	conn.deleteIDs = func(_ context.Context, ids []string) error {
		deletes++
		for _, id := range ids {
			if deleted[id] {
				t.Errorf("identifier %s deleted more than once", id)
			}
			deleted[id] = true
		}
		return nil
	}
	if err := testClient(conn).DeleteByDocID(t.Context(), "doc"); err != nil {
		t.Fatal(err)
	}
	if queries != 3 || deletes != 2 || len(deleted) != 1500 {
		t.Fatalf("queries=%d deletes=%d identifiers=%d", queries, deletes, len(deleted))
	}
}

func TestDeleteRetriesAndMissingVectors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, missing := range []bool{false, true} {
			calls := 0
			client := testClient(&fakeIndexConn{filterDelete: func(context.Context, *structpb.Struct) error {
				calls++
				if calls == 1 {
					return status.Error(codes.Unavailable, "temporary")
				}
				if missing {
					return status.Error(codes.NotFound, "missing")
				}
				return nil
			}})
			if err := client.DeleteByDocID(t.Context(), "doc"); err != nil || calls != 2 {
				t.Fatalf("attempts=%d error=%v", calls, err)
			}
		}
	})
}

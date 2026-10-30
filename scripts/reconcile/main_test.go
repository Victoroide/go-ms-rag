package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/pinecone-io/go-pinecone/v5/pinecone"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

const storedID = "00000000-0000-4000-8000-000000000001"
const missingID = "00000000-0000-4000-8000-000000000002"

func TestOrphanVectorsAreSelected(t *testing.T) {
	refs := []vectorRef{{"missing-metadata", ""}, {"invalid", "not-a-uuid"}, {"missing-row", missingID}, {"stored", storedID}}
	states := map[string]documentState{storedID: {Status: "completed"}}
	require.Equal(t, []string{"missing-metadata", "invalid", "missing-row"}, selectOrphanVectors(refs, states, 10))
}

func TestFailedDocumentsAreOrphans(t *testing.T) {
	for _, tc := range []struct {
		status string
		job    bool
		orphan bool
	}{{"failed", false, true}, {"failed", true, false}, {"processing", false, false}, {"processing", true, false}, {"completed", false, false}} {
		t.Run(fmt.Sprintf("%s/job=%t", tc.status, tc.job), func(t *testing.T) {
			got := selectOrphanVectors([]vectorRef{{"v", storedID}}, map[string]documentState{storedID: {Status: tc.status, HasJob: tc.job}}, 10)
			require.Equal(t, tc.orphan, len(got) == 1)
		})
	}
}

func TestLimitIsRespected(t *testing.T) {
	refs := []vectorRef{{"a", ""}, {"b", ""}, {"c", ""}}
	require.Equal(t, []string{"a", "b"}, selectOrphanVectors(refs, nil, 2))
	require.Empty(t, selectOrphanVectors(refs, nil, 0))
	counts := map[string]uint32{storedID: 3, missingID: 2}
	require.Equal(t, []string{missingID}, selectOrphanNamespaces(counts, nil, 2))
	require.Empty(t, selectOrphanNamespaces(counts, nil, 0))
}

func TestNonUUIDNamespacesAreIgnored(t *testing.T) {
	counts := map[string]uint32{"": 10, "__default__": 10, "shared": 10, storedID: 1, missingID: 1}
	states := map[string]documentState{storedID: {Status: "failed"}}
	require.Equal(t, []string{missingID}, selectOrphanNamespaces(counts, states, 100))
}

type reconcileFakeIndex struct {
	refs       []vectorRef
	counts     map[string]uint32
	listErr    error
	fetchErr   error
	statsErr   error
	deleteErr  error
	listCalls  int
	deleteSize []int
	deleted    []string
	namespaces []string
}

func (f *reconcileFakeIndex) ListVectors(_ context.Context, req *pinecone.ListVectorsRequest) (*pinecone.ListVectorsResponse, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	start := 0
	if req.PaginationToken != nil {
		if _, err := fmt.Sscanf(*req.PaginationToken, "%d", &start); err != nil {
			return nil, err
		}
	}
	end := min(start+int(*req.Limit), len(f.refs))
	res := &pinecone.ListVectorsResponse{}
	for _, ref := range f.refs[start:end] {
		res.VectorIds = append(res.VectorIds, &ref.ID)
	}
	if end < len(f.refs) {
		next := fmt.Sprint(end)
		res.NextPaginationToken = &next
	}
	return res, nil
}

func (f *reconcileFakeIndex) FetchVectors(_ context.Context, ids []string) (*pinecone.FetchVectorsResponse, error) {
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	res := &pinecone.FetchVectorsResponse{Vectors: map[string]*pinecone.Vector{}}
	for _, id := range ids {
		for _, ref := range f.refs {
			if ref.ID == id {
				res.Vectors[id] = &pinecone.Vector{Id: id, Metadata: &structpb.Struct{Fields: map[string]*structpb.Value{"doc_id": structpb.NewStringValue(ref.DocID)}}}
			}
		}
	}
	return res, nil
}

func (f *reconcileFakeIndex) DescribeIndexStats(context.Context) (*pinecone.DescribeIndexStatsResponse, error) {
	res := &pinecone.DescribeIndexStatsResponse{Namespaces: map[string]*pinecone.NamespaceSummary{}}
	for name, count := range f.counts {
		res.Namespaces[name] = &pinecone.NamespaceSummary{VectorCount: count}
	}
	return res, f.statsErr
}

func (f *reconcileFakeIndex) DeleteVectorsById(_ context.Context, ids []string) error { //nolint:revive // Match the SDK method name.
	f.deleteSize = append(f.deleteSize, len(ids))
	f.deleted = append(f.deleted, ids...)
	return f.deleteErr
}

func (f *reconcileFakeIndex) deleteNamespace(_ context.Context, name string) error {
	f.namespaces = append(f.namespaces, name)
	return f.deleteErr
}

func noDocuments(context.Context, []string) (map[string]documentState, error) { return nil, nil }

func TestReconcileDryRunAndApply(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(fmt.Sprint(apply), func(t *testing.T) {
			idx := &reconcileFakeIndex{refs: []vectorRef{{"a", ""}, {"b", missingID}, {"c", storedID}}, counts: map[string]uint32{missingID: 2}}
			lookup := func(context.Context, []string) (map[string]documentState, error) {
				return map[string]documentState{storedID: {Status: "completed"}}, nil
			}
			r, err := reconcile(t.Context(), idx, idx.deleteNamespace, lookup, options{Apply: apply, Limit: 4, Page: 1}, &bytes.Buffer{})
			require.NoError(t, err)
			require.Equal(t, 2, r.OrphanVectors)
			require.Equal(t, 1, r.OrphanNamespaces)
			require.Equal(t, 3, idx.listCalls)
			if apply {
				require.Equal(t, []string{"a", "b"}, idx.deleted)
				require.Equal(t, []string{missingID}, idx.namespaces)
			} else {
				require.Empty(t, idx.deleted)
				require.Empty(t, idx.namespaces)
			}
		})
	}
}

func TestReconcileApplyBatchesAndSharesLimit(t *testing.T) {
	idx := &reconcileFakeIndex{counts: map[string]uint32{missingID: 1}}
	for i := range 2500 {
		idx.refs = append(idx.refs, vectorRef{ID: fmt.Sprint(i)})
	}
	r, err := reconcile(t.Context(), idx, idx.deleteNamespace, noDocuments, options{Apply: true, Limit: 2100, Page: 1000}, &bytes.Buffer{})
	require.NoError(t, err)
	require.Equal(t, 2100, r.OrphanVectors)
	require.Zero(t, r.OrphanNamespaces)
	require.Equal(t, []int{1000, 1000, 100}, idx.deleteSize)
	require.Len(t, idx.deleted, 2100)
	require.Empty(t, idx.namespaces)
	require.LessOrEqual(t, len(r.Sample), 20)
}

func TestReconcileUnsupportedListOnly(t *testing.T) {
	for _, code := range []codes.Code{codes.Unimplemented, codes.InvalidArgument, codes.PermissionDenied, codes.Unavailable} {
		t.Run(code.String(), func(t *testing.T) {
			idx := &reconcileFakeIndex{listErr: status.Error(code, "list unavailable"), counts: map[string]uint32{missingID: 1}}
			var warnings bytes.Buffer
			r, err := reconcile(t.Context(), idx, idx.deleteNamespace, noDocuments, options{Apply: true, Limit: 10, Page: 1}, &warnings)
			if code == codes.Unimplemented {
				require.NoError(t, err)
				require.Equal(t, 1, r.OrphanNamespaces)
				require.Contains(t, warnings.String(), "namespace")
			} else {
				require.Error(t, err)
				require.Empty(t, idx.namespaces)
			}
		})
	}
}

func TestReconcileReadErrorsPreventDeletion(t *testing.T) {
	failure := errors.New("read failed")
	for _, stage := range []string{"fetch", "lookup", "stats"} {
		t.Run(stage, func(t *testing.T) {
			idx := &reconcileFakeIndex{refs: []vectorRef{{"a", missingID}}}
			lookup := noDocuments
			switch stage {
			case "fetch":
				idx.fetchErr = failure
			case "lookup":
				lookup = func(context.Context, []string) (map[string]documentState, error) { return nil, failure }
			case "stats":
				idx.statsErr = failure
			}
			_, err := reconcile(t.Context(), idx, idx.deleteNamespace, lookup, options{Apply: true, Limit: 10, Page: 100}, &bytes.Buffer{})
			require.ErrorIs(t, err, failure)
			require.Empty(t, idx.deleted)
			require.Empty(t, idx.namespaces)
		})
	}
}

func TestReconcileFlags(t *testing.T) {
	opts, err := parseOptions(nil, &bytes.Buffer{})
	require.NoError(t, err)
	require.False(t, opts.Apply)
	require.Equal(t, 100000, opts.Limit)
	require.Equal(t, 100, opts.Page)
	opts, err = parseOptions([]string{"-apply", "-limit=2", "-page=1"}, &bytes.Buffer{})
	require.NoError(t, err)
	require.True(t, opts.Apply)
	require.Equal(t, 2, opts.Limit)
	require.Equal(t, 1, opts.Page)
	for _, args := range [][]string{{"-limit=0"}, {"-limit=-1"}, {"-page=0"}, {"-page=1001"}, {"unexpected"}} {
		_, err = parseOptions(args, &bytes.Buffer{})
		require.Error(t, err)
	}
}

func TestPodListingErrors(t *testing.T) {
	for _, message := range []string{"list is not supported on pod-based indexes", "list is only supported on serverless indexes"} {
		idx := &reconcileFakeIndex{listErr: status.Error(codes.InvalidArgument, message), counts: map[string]uint32{missingID: 1}}
		r, err := reconcile(t.Context(), idx, idx.deleteNamespace, noDocuments, options{Limit: 10, Page: 1}, &bytes.Buffer{})
		require.NoError(t, err)
		require.Equal(t, 1, r.OrphanNamespaces)
		require.Empty(t, idx.namespaces)
	}
}

func TestReconcileDeleteErrorsAreReported(t *testing.T) {
	failure := errors.New("delete failed")
	for _, namespace := range []bool{false, true} {
		idx := &reconcileFakeIndex{deleteErr: failure}
		if namespace {
			idx.counts = map[string]uint32{missingID: 1}
		} else {
			idx.refs = []vectorRef{{"a", ""}}
		}
		_, err := reconcile(t.Context(), idx, idx.deleteNamespace, noDocuments, options{Apply: true, Limit: 10, Page: 1}, &bytes.Buffer{})
		require.ErrorIs(t, err, failure)
	}
}

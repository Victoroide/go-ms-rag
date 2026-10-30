package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/pinecone-io/go-pinecone/v5/pinecone"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type vectorRef struct {
	ID    string
	DocID string
}

type documentState struct {
	Status string
	HasJob bool
}

type documentLookup func(context.Context, []string) (map[string]documentState, error)

type vectorIndex interface {
	ListVectors(context.Context, *pinecone.ListVectorsRequest) (*pinecone.ListVectorsResponse, error)
	FetchVectors(context.Context, []string) (*pinecone.FetchVectorsResponse, error)
	DescribeIndexStats(context.Context) (*pinecone.DescribeIndexStatsResponse, error)
	DeleteVectorsById(context.Context, []string) error
}

type report struct {
	OrphanVectors    int      `json:"orphan_vectors"`
	OrphanNamespaces int      `json:"orphan_namespaces"`
	Sample           []string `json:"sample"`
}

func selectOrphanVectors(refs []vectorRef, states map[string]documentState, limit int) []string {
	var ids []string
	for _, ref := range refs {
		if len(ids) >= limit {
			break
		}
		id, err := uuid.Parse(ref.DocID)
		state, exists := states[id.String()]
		if err != nil || !exists || (state.Status == "failed" && !state.HasJob) {
			ids = append(ids, ref.ID)
		}
	}
	return ids
}

func selectOrphanNamespaces(counts map[string]uint32, states map[string]documentState, limit int) []string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	var selected []string
	remaining := uint64(max(limit, 0))
	for _, name := range names {
		id, err := uuid.Parse(name)
		if err != nil {
			continue
		}
		if _, exists := states[id.String()]; exists {
			continue
		}
		cost := max(uint64(counts[name]), 1)
		if cost > remaining {
			continue
		}
		selected = append(selected, name)
		remaining -= cost
	}
	return selected
}

func reconcile(ctx context.Context, idx vectorIndex, deleteNamespace func(context.Context, string) error, lookup documentLookup, opts options, warnings io.Writer) (report, error) {
	result := report{Sample: []string{}}
	stats, err := idx.DescribeIndexStats(ctx)
	if err != nil {
		return result, fmt.Errorf("describe index statistics: %w", err)
	}
	ids, err := scanVectors(ctx, idx, lookup, opts, warnings)
	if err != nil {
		return result, err
	}
	counts := make(map[string]uint32)
	var names []string
	for name, summary := range stats.Namespaces {
		if _, parseErr := uuid.Parse(name); parseErr == nil && summary != nil {
			counts[name] = summary.VectorCount
			names = append(names, name)
		}
	}
	states := make(map[string]documentState)
	for start := 0; start < len(names); start += 1000 {
		batch, lookupErr := lookup(ctx, names[start:min(start+1000, len(names))])
		if lookupErr != nil {
			return result, lookupErr
		}
		for id, state := range batch {
			states[id] = state
		}
	}
	namespaces := selectOrphanNamespaces(counts, states, opts.Limit-len(ids))
	result.OrphanVectors = len(ids)
	result.OrphanNamespaces = len(namespaces)
	for _, id := range ids[:min(len(ids), 20)] {
		result.Sample = append(result.Sample, "vector:"+id)
	}
	for _, name := range namespaces[:min(len(namespaces), 20-len(result.Sample))] {
		result.Sample = append(result.Sample, "namespace:"+name)
	}
	if !opts.Apply {
		return result, nil
	}
	for start := 0; start < len(ids); start += 1000 {
		if err := idx.DeleteVectorsById(ctx, ids[start:min(start+1000, len(ids))]); err != nil {
			return result, fmt.Errorf("delete orphaned vectors: %w", err)
		}
	}
	for _, name := range namespaces {
		if err := deleteNamespace(ctx, name); err != nil {
			return result, fmt.Errorf("delete orphaned namespace %q: %w", name, err)
		}
	}
	return result, nil
}

func scanVectors(ctx context.Context, idx vectorIndex, lookup documentLookup, opts options, warnings io.Writer) ([]string, error) {
	var selected []string
	var token *string
	seen := make(map[string]bool)
	for len(selected) < opts.Limit {
		pageSize := min(opts.Page, opts.Limit-len(selected))
		if pageSize < 1 || pageSize > 1000 {
			return nil, fmt.Errorf("vector listing page size must be between 1 and 1000")
		}
		page := uint32(pageSize)
		res, err := idx.ListVectors(ctx, &pinecone.ListVectorsRequest{Limit: &page, PaginationToken: token})
		if err != nil {
			if listUnsupported(err) {
				if _, writeErr := fmt.Fprintln(warnings, "vector listing is unsupported; reconciling namespaces only"); writeErr != nil {
					return nil, writeErr
				}
				return nil, nil
			}
			return nil, fmt.Errorf("list vectors: %w", err)
		}
		var ids []string
		for _, id := range res.VectorIds {
			if id != nil && *id != "" {
				ids = append(ids, *id)
			}
		}
		if len(ids) > 0 {
			refs, fetchErr := fetchReferences(ctx, idx, ids)
			if fetchErr != nil {
				return nil, fetchErr
			}
			docIDs := make([]string, 0, len(refs))
			for _, ref := range refs {
				docIDs = append(docIDs, ref.DocID)
			}
			states, lookupErr := lookup(ctx, docIDs)
			if lookupErr != nil {
				return nil, lookupErr
			}
			selected = append(selected, selectOrphanVectors(refs, states, opts.Limit-len(selected))...)
		}
		token = res.NextPaginationToken
		if token == nil || *token == "" {
			break
		}
		if seen[*token] {
			return nil, fmt.Errorf("vector listing repeated a pagination token")
		}
		seen[*token] = true
	}
	return selected, nil
}

func fetchReferences(ctx context.Context, idx vectorIndex, ids []string) ([]vectorRef, error) {
	res, err := idx.FetchVectors(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("fetch vector metadata: %w", err)
	}
	refs := make([]vectorRef, 0, len(ids))
	for _, id := range ids {
		vector := res.Vectors[id]
		// A vector can disappear between listing and fetching.
		if vector == nil {
			continue
		}
		ref := vectorRef{ID: id}
		if vector.Metadata != nil {
			ref.DocID = vector.Metadata.GetFields()["doc_id"].GetStringValue()
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func listUnsupported(err error) bool {
	if status.Code(err) == codes.Unimplemented {
		return true
	}
	if status.Code(err) != codes.InvalidArgument && status.Code(err) != codes.FailedPrecondition {
		return false
	}
	message := strings.ToLower(status.Convert(err).Message())
	return strings.Contains(message, "list") && (strings.Contains(message, "not supported") ||
		(strings.Contains(message, "only supported") && strings.Contains(message, "serverless")))
}

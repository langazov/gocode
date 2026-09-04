package rag

import (
	"context"
	"fmt"
	"strings"

	"github.com/langazov/gocode-go/internal/rag/embed"
	"github.com/langazov/gocode-go/internal/rag/store"
)

// SearchOptions configures one search call.
type SearchOptions struct {
	Query      string
	K          int
	PathPrefix string
}

// SearchHit is one ranked chunk, ready to cite.
type SearchHit struct {
	Path      string
	StartLine int
	EndLine   int
	Content   string
	Score     float32
}

// Searcher embeds a query and ranks a project's stored chunks against it.
type Searcher struct {
	Store     *store.Store
	Embedder  *embed.Client
	ProjectID string
}

// Search embeds opts.Query and returns the top opts.K chunks, most similar
// first.
func (s *Searcher) Search(ctx context.Context, opts SearchOptions) ([]SearchHit, error) {
	if strings.TrimSpace(opts.Query) == "" {
		return nil, fmt.Errorf("rag: query is required")
	}
	vectors, err := s.Embedder.Embed(ctx, []string{opts.Query})
	if err != nil {
		return nil, fmt.Errorf("rag: embed query: %w", err)
	}
	results, err := s.Store.Search(ctx, s.ProjectID, vectors[0], opts.K, opts.PathPrefix)
	if err != nil {
		return nil, fmt.Errorf("rag: search: %w", err)
	}
	hits := make([]SearchHit, len(results))
	for i, r := range results {
		hits[i] = SearchHit{
			Path:      r.Path,
			StartLine: r.StartLine,
			EndLine:   r.EndLine,
			Content:   r.Content,
			Score:     r.Score,
		}
	}
	return hits, nil
}

// FormatHits renders search hits as citeable text: a "path:start-end" header
// per chunk followed by its content, the same convention the read tool's
// "N: content" line numbering already primes the model to cite locations
// with.
func FormatHits(hits []SearchHit) string {
	if len(hits) == 0 {
		return "No matching chunks found."
	}
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%s:%d-%d\n%s", h.Path, h.StartLine, h.EndLine, h.Content)
	}
	return b.String()
}

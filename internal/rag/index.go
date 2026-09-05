// Package rag orchestrates chunking, embedding, and storage into the two
// operations the plugin's tools expose: (re)indexing a directory and
// searching it. The three lower-level packages (chunk, embed, store) stay
// independently testable; this package is the thin glue between them.
package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/langazov/gocode-go/internal/rag/chunk"
	"github.com/langazov/gocode-go/internal/rag/embed"
	"github.com/langazov/gocode-go/internal/rag/store"
)

// IndexOptions configures one indexing pass.
type IndexOptions struct {
	// Root is the absolute directory to walk.
	Root string
	// Scope is Root's project-root-relative path (slash-separated, no
	// leading/trailing slash). Empty means Root is the project root itself,
	// i.e. a whole-project index. Set it when Root is a subdirectory being
	// indexed on its own, so chunk paths/IDs come out identical to what a
	// whole-project walk would produce, and so the stale-chunk diff only
	// considers chunks already stored under that subtree — otherwise every
	// chunk from the rest of the project would look stale and get deleted.
	Scope string
	// Force re-embeds every chunk even if its content hash matches what is
	// already stored — useful after switching embedding models.
	Force        bool
	Include      []string
	Exclude      []string
	ChunkLines   int
	ChunkOverlap int
}

// IndexSummary reports what one Index call changed.
type IndexSummary struct {
	FilesScanned  int `json:"filesScanned"`
	ChunksAdded   int `json:"chunksAdded"`
	ChunksUpdated int `json:"chunksUpdated"`
	ChunksRemoved int `json:"chunksRemoved"`
}

// String renders the summary as the tool's JSON output text. Marshal cannot
// fail on this all-int struct, so there is no fallback path to keep in sync.
func (s IndexSummary) String() string {
	data, _ := json.Marshal(s)
	return string(data)
}

// Indexer ties chunking, embedding, and storage together for one project.
type Indexer struct {
	Store     *store.Store
	Embedder  *embed.Client
	ProjectID string
	// LSP, when set, is passed through to chunk.Walk for syntax-aware
	// splitting; nil (the zero value) means every file uses the
	// language-agnostic sliding window, unchanged from before this field
	// existed.
	LSP chunk.SymbolResolver
}

// Index (re)indexes opts.Root: walks and chunks the tree, embeds only chunks
// whose content changed since the last index (or every chunk if opts.Force),
// and removes chunks that no longer exist — whether their file was deleted,
// no longer matches the include/exclude filters, or its line count shifted
// enough to change which chunk IDs its windows produce.
//
// Diffing is entirely ID-based (existing chunk IDs vs. the fresh walk's IDs)
// rather than path-based: a chunk ID already encodes (path, start, end), so
// this one comparison naturally covers "file deleted," "file excluded," and
// "file shrank/grew enough to shift window boundaries" without three separate
// code paths. When opts.Scope is set, both sides of that comparison are
// restricted to the scoped subtree, so indexing one subdirectory never marks
// the rest of the project's chunks stale.
func (idx *Indexer) Index(ctx context.Context, opts IndexOptions) (IndexSummary, error) {
	chunks, err := chunk.Walk(ctx, opts.Root, chunk.Options{
		Include:    opts.Include,
		Exclude:    opts.Exclude,
		Lines:      opts.ChunkLines,
		Overlap:    opts.ChunkOverlap,
		LSP:        idx.LSP,
		PathPrefix: opts.Scope,
	})
	if err != nil {
		return IndexSummary{}, fmt.Errorf("rag: walk %s: %w", opts.Root, err)
	}

	existingHashes, err := idx.Store.HashesUnderPath(ctx, idx.ProjectID, opts.Scope)
	if err != nil {
		return IndexSummary{}, fmt.Errorf("rag: load existing hashes: %w", err)
	}

	var summary IndexSummary
	seenPaths := map[string]bool{}
	seenIDs := map[string]bool{}
	var toEmbed []chunk.Chunk
	for _, c := range chunks {
		seenPaths[c.Path] = true
		seenIDs[c.ID] = true
		oldHash, existed := existingHashes[c.ID]
		if opts.Force || !existed || oldHash != c.ContentHash {
			toEmbed = append(toEmbed, c)
			if existed {
				summary.ChunksUpdated++
			} else {
				summary.ChunksAdded++
			}
		}
	}
	summary.FilesScanned = len(seenPaths)

	var staleIDs []string
	for id := range existingHashes {
		if !seenIDs[id] {
			staleIDs = append(staleIDs, id)
		}
	}
	summary.ChunksRemoved = len(staleIDs)

	if len(toEmbed) > 0 {
		texts := make([]string, len(toEmbed))
		for i, c := range toEmbed {
			texts[i] = c.Content
		}
		vectors, err := idx.Embedder.Embed(ctx, texts)
		if err != nil {
			return IndexSummary{}, fmt.Errorf("rag: embed %d chunks: %w", len(toEmbed), err)
		}
		now := time.Now().Unix()
		records := make([]store.Record, len(toEmbed))
		for i, c := range toEmbed {
			records[i] = store.Record{
				ID:          c.ID,
				ProjectID:   idx.ProjectID,
				Path:        c.Path,
				StartLine:   c.StartLine,
				EndLine:     c.EndLine,
				Content:     c.Content,
				ContentHash: c.ContentHash,
				Embedding:   vectors[i],
				UpdatedAt:   now,
			}
		}
		if err := idx.Store.Put(ctx, records); err != nil {
			return IndexSummary{}, fmt.Errorf("rag: store %d chunks: %w", len(records), err)
		}
	}

	if len(staleIDs) > 0 {
		if err := idx.Store.DeleteIDs(ctx, idx.ProjectID, staleIDs); err != nil {
			return IndexSummary{}, fmt.Errorf("rag: remove %d stale chunks: %w", len(staleIDs), err)
		}
	}

	return summary, nil
}

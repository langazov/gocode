// This file wires internal/rag/eval into the CLI as `rag-plugin eval
// retrieval` and `rag-plugin eval chunks`. See that package's doc comment
// for the methodology; this file is just flag parsing and formatting.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/langazov/gocode-go/internal/lsp"
	"github.com/langazov/gocode-go/internal/rag"
	"github.com/langazov/gocode-go/internal/rag/chunk"
	"github.com/langazov/gocode-go/internal/rag/eval"
)

func runCLIEval(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: rag-plugin eval <retrieval|chunks> [flags]")
	}
	switch args[0] {
	case "retrieval":
		return runCLIEvalRetrieval(args[1:])
	case "chunks":
		return runCLIEvalChunks(args[1:])
	default:
		return fmt.Errorf("unknown eval subcommand %q: want \"retrieval\" or \"chunks\"", args[0])
	}
}

// runCLIEvalRetrieval scores an already-indexed project's rag_search against
// a gold set mined from its own commit history, reporting Recall@K, MRR, and
// NDCG@K with bootstrap confidence intervals instead of a spot-checked
// impression.
func runCLIEvalRetrieval(args []string) error {
	fs := flag.NewFlagSet("rag-plugin eval retrieval", flag.ContinueOnError)
	root := fs.String("root", ".", "project root; must already be indexed (see rag-plugin index)")
	project := fs.String("project", "", "project id; defaults to the resolved absolute root")
	k := fs.Int("k", 8, "how many results rag_search returns per query")
	maxCommits := fs.Int("max-commits", 300, "how many recent non-merge commits to mine for gold pairs")
	minSubjectLen := fs.Int("min-subject-len", 15, "skip commit subjects shorter than this many characters")
	maxFilesPerCommit := fs.Int("max-files-per-commit", 3, "skip commits touching more files than this")
	bootstrapN := fs.Int("bootstrap", 2000, "bootstrap resamples used for the confidence interval")
	dbPath := fs.String("db", "", "sqlite path; defaults to $GOCODE_DATA/rag.db")
	embProvider := fs.String("embedding-provider", "", "models.dev provider id, or gocoder")
	embModel := fs.String("embedding-model", "", "embedding model id")
	embBaseURL := fs.String("embedding-base-url", "", "override the embeddings endpoint")
	asJSON := fs.Bool("json", false, "emit metrics as JSON")
	verbose := fs.Bool("v", false, "print every gold pair's outcome (query and rank)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	projectID := *project
	if projectID == "" {
		projectID = absRoot
	}

	ctx := context.Background()
	gold, err := eval.MineGoldSet(ctx, absRoot, eval.MineOptions{
		MaxCommits:        *maxCommits,
		MinSubjectLen:     *minSubjectLen,
		MaxFilesPerCommit: *maxFilesPerCommit,
	})
	if err != nil {
		return fmt.Errorf("mine gold set: %w", err)
	}
	if len(gold) == 0 {
		return fmt.Errorf("no usable gold pairs mined from history in %d commit(s); try a larger -max-commits or a smaller -min-subject-len", *maxCommits)
	}

	r, err := buildRuntime(ctx, runtimeOptions{
		Directory:         absRoot,
		Worktree:          absRoot,
		ProjectID:         projectID,
		DBPath:            *dbPath,
		EmbeddingProvider: *embProvider,
		EmbeddingModel:    *embModel,
		EmbeddingBaseURL:  *embBaseURL,
	})
	if err != nil {
		return err
	}
	defer r.close()

	search := func(ctx context.Context, query string, k int) ([]eval.Hit, error) {
		hits, err := r.searcher.Search(ctx, rag.SearchOptions{Query: query, K: k})
		if err != nil {
			return nil, err
		}
		out := make([]eval.Hit, len(hits))
		for i, h := range hits {
			out[i] = eval.Hit{Path: h.Path, StartLine: h.StartLine, EndLine: h.EndLine}
		}
		return out, nil
	}

	metrics, scores, err := eval.Evaluate(ctx, gold, search, *k, *bootstrapN)
	if err != nil {
		return err
	}

	if *verbose {
		for _, s := range scores {
			status := "MISS"
			if s.Rank > 0 {
				status = fmt.Sprintf("rank %d", s.Rank)
			}
			fmt.Printf("%-8s %s\n", status, s.Gold.Query)
		}
		fmt.Println()
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(metrics)
	}
	fmt.Printf("gold pairs: %d (mined from up to %d commits)\n", metrics.N, *maxCommits)
	fmt.Printf("Recall@%-2d %.3f  [%.3f, %.3f] 95%% CI\n", metrics.K, metrics.RecallAtK.Mean, metrics.RecallAtK.CILow, metrics.RecallAtK.CIHigh)
	fmt.Printf("MRR       %.3f  [%.3f, %.3f] 95%% CI\n", metrics.MRR.Mean, metrics.MRR.CILow, metrics.MRR.CIHigh)
	fmt.Printf("NDCG@%-3d %.3f  [%.3f, %.3f] 95%% CI\n", metrics.K, metrics.NDCG.Mean, metrics.NDCG.CILow, metrics.NDCG.CIHigh)
	return nil
}

// runCLIEvalChunks scores chunk.Walk's boundary quality against real
// function/class/method boundaries, in both the plain sliding-window mode
// and syntax-aware mode, so the trade-off chunk.go's package doc describes
// qualitatively becomes two comparable numbers.
func runCLIEvalChunks(args []string) error {
	fs := flag.NewFlagSet("rag-plugin eval chunks", flag.ContinueOnError)
	root := fs.String("root", ".", "directory to scan")
	include := fs.String("include", "", "comma-separated include globs")
	exclude := fs.String("exclude", "", "comma-separated exclude globs")
	chunkLines := fs.Int("chunk-lines", 0, "chunk size in source lines (0 = default 60)")
	chunkOverlap := fs.Int("chunk-overlap", 0, "overlap between adjacent chunks (0 = default 10)")
	noGitignore := fs.Bool("no-gitignore", false, "don't honor .gitignore/.ignore files")
	asJSON := fs.Bool("json", false, "emit both reports as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}

	exc := splitCSV(*exclude)
	if exc == nil {
		exc = defaultExclude
	}
	base := chunk.Options{
		Include:          splitCSV(*include),
		Exclude:          exc,
		Lines:            *chunkLines,
		Overlap:          *chunkOverlap,
		DisableGitignore: *noGitignore,
	}

	ctx := context.Background()
	lspSvc := lsp.New(absRoot, nil)
	defer lspSvc.Shutdown()

	slidingOpts := base
	slidingOpts.LSP = nil
	sliding, err := eval.EvaluateChunking(ctx, absRoot, lspSvc, slidingOpts)
	if err != nil {
		return fmt.Errorf("evaluate sliding-window chunking: %w", err)
	}

	syntaxOpts := base
	syntaxOpts.LSP = lspSvc
	syntaxAware, err := eval.EvaluateChunking(ctx, absRoot, lspSvc, syntaxOpts)
	if err != nil {
		return fmt.Errorf("evaluate syntax-aware chunking: %w", err)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]eval.BoundaryReport{"slidingWindow": sliding, "syntaxAware": syntaxAware})
	}

	printReport := func(name string, r eval.BoundaryReport) {
		fmt.Printf("%s: %d file(s) with symbols, %d symbol(s)\n", name, r.Files, r.Symbols)
		fmt.Printf("  boundary integrity: %.3f (fully-contained symbols / total)\n", r.IntegrityRate())
		fmt.Printf("  containment ratio:  p50=%.3f  p10=%.3f\n", r.Percentile(0.5), r.Percentile(0.1))
	}
	printReport("sliding window", sliding)
	printReport("syntax-aware  ", syntaxAware)
	return nil
}

package eval

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestFirstRelevantRank(t *testing.T) {
	gold := GoldPair{Relevant: []Region{{Path: "a.go", StartLine: 10, EndLine: 20}}}
	hits := []Hit{
		{Path: "b.go", StartLine: 1, EndLine: 5},
		{Path: "a.go", StartLine: 15, EndLine: 25}, // overlaps 15-20
		{Path: "a.go", StartLine: 30, EndLine: 40},
	}
	if rank := firstRelevantRank(gold, hits); rank != 2 {
		t.Fatalf("got rank %d, want 2", rank)
	}

	noHit := []Hit{{Path: "c.go", StartLine: 1, EndLine: 5}}
	if rank := firstRelevantRank(gold, noHit); rank != 0 {
		t.Fatalf("got rank %d, want 0 (no overlap)", rank)
	}
}

// TestFirstRelevantRankMatchesAnyOfMultipleRelevantRegions covers a gold
// pair with more than one Relevant region — the normal shape MineGoldSet
// produces for a commit touching several files (up to MaxFilesPerCommit).
// A hit overlapping the *second* region, not the first, must still count,
// and at the hit's own rank.
func TestFirstRelevantRankMatchesAnyOfMultipleRelevantRegions(t *testing.T) {
	gold := GoldPair{Relevant: []Region{
		{Path: "a.go", StartLine: 1, EndLine: 5},
		{Path: "b.go", StartLine: 10, EndLine: 20},
	}}
	hits := []Hit{
		{Path: "x.go", StartLine: 1, EndLine: 5},   // no overlap
		{Path: "c.go", StartLine: 1, EndLine: 5},   // no overlap
		{Path: "b.go", StartLine: 15, EndLine: 25}, // overlaps the second region
	}
	if rank := firstRelevantRank(gold, hits); rank != 3 {
		t.Fatalf("got rank %d, want 3 (the second relevant region's match)", rank)
	}
}

func TestRegionOverlaps(t *testing.T) {
	tests := []struct {
		name     string
		a, b     Region
		wantOver bool
	}{
		{"identical range", Region{"a.go", 10, 20}, Region{"a.go", 10, 20}, true},
		{"a contains b", Region{"a.go", 1, 100}, Region{"a.go", 10, 20}, true},
		{"touching at one line", Region{"a.go", 1, 10}, Region{"a.go", 10, 20}, true},
		{"adjacent, no shared line", Region{"a.go", 1, 10}, Region{"a.go", 11, 20}, false},
		{"disjoint", Region{"a.go", 1, 5}, Region{"a.go", 50, 60}, false},
		{"same lines, different file", Region{"a.go", 1, 10}, Region{"b.go", 1, 10}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Overlaps(tt.b); got != tt.wantOver {
				t.Errorf("%+v.Overlaps(%+v) = %v, want %v", tt.a, tt.b, got, tt.wantOver)
			}
			// Overlaps must be symmetric.
			if got := tt.b.Overlaps(tt.a); got != tt.wantOver {
				t.Errorf("%+v.Overlaps(%+v) = %v, want %v (not symmetric)", tt.b, tt.a, got, tt.wantOver)
			}
		})
	}
}

func TestEvaluateComputesRecallAndMRR(t *testing.T) {
	gold := []GoldPair{
		{Query: "q1", Relevant: []Region{{Path: "a.go", StartLine: 1, EndLine: 10}}},
		{Query: "q2", Relevant: []Region{{Path: "b.go", StartLine: 1, EndLine: 10}}},
	}
	search := func(_ context.Context, query string, k int) ([]Hit, error) {
		switch query {
		case "q1":
			// relevant result ranked first
			return []Hit{{Path: "a.go", StartLine: 1, EndLine: 10}, {Path: "x.go", StartLine: 1, EndLine: 5}}, nil
		default:
			// relevant result ranked second
			return []Hit{{Path: "x.go", StartLine: 1, EndLine: 5}, {Path: "b.go", StartLine: 1, EndLine: 10}}, nil
		}
	}

	metrics, scores, err := Evaluate(context.Background(), gold, search, 8, 500)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.N != 2 || metrics.K != 8 {
		t.Fatalf("unexpected metrics header: %+v", metrics)
	}
	if metrics.RecallAtK.Mean != 1.0 {
		t.Fatalf("recall@8 = %v, want 1.0 (both queries hit)", metrics.RecallAtK.Mean)
	}
	wantMRR := (1.0 + 0.5) / 2
	if math.Abs(metrics.MRR.Mean-wantMRR) > 1e-9 {
		t.Fatalf("mrr = %v, want %v", metrics.MRR.Mean, wantMRR)
	}
	if scores[0].Rank != 1 || scores[1].Rank != 2 {
		t.Fatalf("unexpected ranks: %+v", scores)
	}
	// CI must bracket the observed mean for a two-point sample.
	if metrics.RecallAtK.CILow > metrics.RecallAtK.Mean || metrics.RecallAtK.CIHigh < metrics.RecallAtK.Mean {
		t.Fatalf("CI does not bracket the mean: %+v", metrics.RecallAtK)
	}
}

func TestEvaluateRejectsEmptyGoldSet(t *testing.T) {
	_, _, err := Evaluate(context.Background(), nil, func(context.Context, string, int) ([]Hit, error) { return nil, nil }, 8, 0)
	if err == nil {
		t.Fatal("expected an error for an empty gold set")
	}
}

// TestEvaluatePropagatesSearchError checks that a single failing query
// aborts the whole run with an error naming that query, rather than
// silently scoring the rest and hiding the failure in a partial result.
func TestEvaluatePropagatesSearchError(t *testing.T) {
	gold := []GoldPair{
		{Query: "q1", Relevant: []Region{{Path: "a.go", StartLine: 1, EndLine: 10}}},
		{Query: "q2 (boom)", Relevant: []Region{{Path: "b.go", StartLine: 1, EndLine: 10}}},
	}
	wantErr := errors.New("embeddings endpoint unavailable")
	search := func(_ context.Context, query string, k int) ([]Hit, error) {
		if query == "q2 (boom)" {
			return nil, wantErr
		}
		return []Hit{{Path: "a.go", StartLine: 1, EndLine: 10}}, nil
	}

	_, _, err := Evaluate(context.Background(), gold, search, 8, 0)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error does not wrap the underlying search error: %v", err)
	}
	if !strings.Contains(err.Error(), "q2 (boom)") {
		t.Errorf("error %q does not name the failing query", err.Error())
	}
}

// TestEvaluateForwardsKToSearchFunc guards against a wiring bug (a
// hardcoded k, or bootstrapIterations passed where k belongs): every call
// to search must receive exactly the k the caller passed to Evaluate.
func TestEvaluateForwardsKToSearchFunc(t *testing.T) {
	gold := []GoldPair{
		{Query: "q1", Relevant: []Region{{Path: "a.go", StartLine: 1, EndLine: 10}}},
	}
	var gotK int
	search := func(_ context.Context, _ string, k int) ([]Hit, error) {
		gotK = k
		return nil, nil
	}
	if _, _, err := Evaluate(context.Background(), gold, search, 42, 10); err != nil {
		t.Fatal(err)
	}
	if gotK != 42 {
		t.Errorf("search received k=%d, want 42", gotK)
	}
}

// TestEvaluateHandlesFewerHitsThanK guards against an index-out-of-range or
// miscount when a search legitimately returns fewer hits than K (a small
// or sparsely-indexed project) — a real, common case, not a malformed
// input.
func TestEvaluateHandlesFewerHitsThanK(t *testing.T) {
	gold := []GoldPair{
		{Query: "q1", Relevant: []Region{{Path: "a.go", StartLine: 1, EndLine: 10}}},
	}
	search := func(context.Context, string, int) ([]Hit, error) {
		return []Hit{{Path: "a.go", StartLine: 1, EndLine: 10}}, nil // 1 hit, K=8
	}
	metrics, scores, err := Evaluate(context.Background(), gold, search, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	if scores[0].Rank != 1 {
		t.Fatalf("rank = %d, want 1", scores[0].Rank)
	}
	if metrics.RecallAtK.Mean != 1.0 {
		t.Fatalf("recall = %v, want 1.0", metrics.RecallAtK.Mean)
	}
}

// TestEvaluateSingleGoldPairDegenerateCI exercises bootstrap's n==1 branch
// (see its doc comment): a lone gold pair has nothing to resample, so the
// "confidence interval" must degenerate to the single observed value
// rather than dividing by zero or fabricating spread from one sample.
func TestNDCGAtKPerfectRankingIsOne(t *testing.T) {
	gold := GoldPair{Relevant: []Region{{Path: "a.go", StartLine: 1, EndLine: 10}}}
	hits := []Hit{{Path: "a.go", StartLine: 1, EndLine: 10}}
	if got := ndcgAtK(gold, hits, 8); got != 1.0 {
		t.Fatalf("got %v, want 1.0 (the one relevant region found at rank 1)", got)
	}
}

func TestNDCGAtKDecaysWithRank(t *testing.T) {
	gold := GoldPair{Relevant: []Region{{Path: "a.go", StartLine: 1, EndLine: 10}}}
	// Relevant hit at rank 3 (two irrelevant hits ahead of it).
	hits := []Hit{
		{Path: "x.go", StartLine: 1, EndLine: 5},
		{Path: "y.go", StartLine: 1, EndLine: 5},
		{Path: "a.go", StartLine: 1, EndLine: 10},
	}
	got := ndcgAtK(gold, hits, 8)
	want := 1 / math.Log2(4) // IDCG=1 (one relevant region, k>=1); DCG=1/log2(rank+1)=1/log2(4)=0.5
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %v, want %v", got, want)
	}
	// And it must decay monotonically: a rank-3 hit scores worse than rank-1.
	if perfect := ndcgAtK(gold, []Hit{{Path: "a.go", StartLine: 1, EndLine: 10}}, 8); got >= perfect {
		t.Fatalf("rank-3 NDCG (%v) should be less than rank-1 NDCG (%v)", got, perfect)
	}
}

func TestNDCGAtKZeroWhenNothingFound(t *testing.T) {
	gold := GoldPair{Relevant: []Region{{Path: "a.go", StartLine: 1, EndLine: 10}}}
	hits := []Hit{{Path: "x.go", StartLine: 1, EndLine: 5}, {Path: "y.go", StartLine: 1, EndLine: 5}}
	if got := ndcgAtK(gold, hits, 8); got != 0 {
		t.Fatalf("got %v, want 0 (no hit overlaps the relevant region)", got)
	}
}

// TestNDCGAtKDoesNotDoubleCreditTheSameRegion is the correctness case that
// motivated crediting each relevant region only once, to its
// highest-ranked match: with a nonzero chunk overlap, two adjacent chunks
// routinely both overlap the same single diff hunk. Without
// once-per-region crediting, both would add gain for what is really one
// answer, and NDCG could exceed 1.0 — breaking the "normalized" in its
// name.
func TestNDCGAtKDoesNotDoubleCreditTheSameRegion(t *testing.T) {
	gold := GoldPair{Relevant: []Region{{Path: "a.go", StartLine: 1, EndLine: 20}}}
	hits := []Hit{
		{Path: "a.go", StartLine: 1, EndLine: 10},  // overlaps the region
		{Path: "a.go", StartLine: 10, EndLine: 20}, // also overlaps the same region
	}
	got := ndcgAtK(gold, hits, 8)
	if got != 1.0 {
		t.Fatalf("got %v, want 1.0 — a second hit on the same already-credited region must add no extra gain", got)
	}
}

// TestNDCGAtKRewardsFindingMultipleDistinctRegions covers a gold pair from
// a multi-file commit (up to MaxFilesPerCommit files): finding two
// genuinely distinct relevant regions is worth more than finding only one,
// unlike Rank/MRR which only ever look at the first match.
func TestNDCGAtKRewardsFindingMultipleDistinctRegions(t *testing.T) {
	gold := GoldPair{Relevant: []Region{
		{Path: "a.go", StartLine: 1, EndLine: 10},
		{Path: "b.go", StartLine: 1, EndLine: 10},
	}}
	oneFound := []Hit{{Path: "a.go", StartLine: 1, EndLine: 10}}
	bothFound := []Hit{
		{Path: "a.go", StartLine: 1, EndLine: 10},
		{Path: "b.go", StartLine: 1, EndLine: 10},
	}
	ndcgOne := ndcgAtK(gold, oneFound, 8)
	ndcgBoth := ndcgAtK(gold, bothFound, 8)
	if ndcgBoth <= ndcgOne {
		t.Fatalf("finding both regions (%v) should score higher than finding only one (%v)", ndcgBoth, ndcgOne)
	}
	if ndcgBoth != 1.0 {
		t.Fatalf("both regions found at the ideal ranks 1 and 2: got %v, want 1.0", ndcgBoth)
	}
}

func TestNDCGAtKCapsIdealAtK(t *testing.T) {
	// Three relevant regions but k=1: the ideal ranking can only fit one of
	// them in the top 1, so IDCG must be capped at k, not len(Relevant).
	gold := GoldPair{Relevant: []Region{
		{Path: "a.go", StartLine: 1, EndLine: 10},
		{Path: "b.go", StartLine: 1, EndLine: 10},
		{Path: "c.go", StartLine: 1, EndLine: 10},
	}}
	hits := []Hit{{Path: "a.go", StartLine: 1, EndLine: 10}}
	if got := ndcgAtK(gold, hits, 1); got != 1.0 {
		t.Fatalf("got %v, want 1.0 (the single available slot was used optimally)", got)
	}
}

func TestEvaluateComputesNDCG(t *testing.T) {
	gold := []GoldPair{
		{Query: "q1", Relevant: []Region{{Path: "a.go", StartLine: 1, EndLine: 10}}},
		{Query: "q2", Relevant: []Region{{Path: "b.go", StartLine: 1, EndLine: 10}}},
	}
	search := func(_ context.Context, query string, k int) ([]Hit, error) {
		if query == "q1" {
			return []Hit{{Path: "a.go", StartLine: 1, EndLine: 10}}, nil // rank 1
		}
		return []Hit{{Path: "x.go", StartLine: 1, EndLine: 5}, {Path: "b.go", StartLine: 1, EndLine: 10}}, nil // rank 2
	}
	metrics, scores, err := Evaluate(context.Background(), gold, search, 8, 500)
	if err != nil {
		t.Fatal(err)
	}
	wantNDCG := (1.0 + 1/math.Log2(3)) / 2
	if math.Abs(metrics.NDCG.Mean-wantNDCG) > 1e-9 {
		t.Fatalf("NDCG.Mean = %v, want %v", metrics.NDCG.Mean, wantNDCG)
	}
	if scores[0].NDCG != 1.0 {
		t.Errorf("scores[0].NDCG = %v, want 1.0", scores[0].NDCG)
	}
	if math.Abs(scores[1].NDCG-1/math.Log2(3)) > 1e-9 {
		t.Errorf("scores[1].NDCG = %v, want %v", scores[1].NDCG, 1/math.Log2(3))
	}
}

func TestEvaluateSingleGoldPairDegenerateCI(t *testing.T) {
	gold := []GoldPair{{Query: "q1", Relevant: []Region{{Path: "a.go", StartLine: 1, EndLine: 10}}}}
	search := func(context.Context, string, int) ([]Hit, error) {
		return []Hit{{Path: "a.go", StartLine: 1, EndLine: 10}}, nil
	}
	metrics, _, err := Evaluate(context.Background(), gold, search, 8, 500)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.RecallAtK.CILow != 1 || metrics.RecallAtK.CIHigh != 1 {
		t.Fatalf("single-sample CI = [%v, %v], want [1, 1]", metrics.RecallAtK.CILow, metrics.RecallAtK.CIHigh)
	}
}

func TestBootstrapConstantValues(t *testing.T) {
	values := []float64{1, 1, 1, 1}
	stat := bootstrap(values, 1000)
	if stat.Mean != 1 || stat.CILow != 1 || stat.CIHigh != 1 {
		t.Fatalf("constant input should produce a degenerate CI, got %+v", stat)
	}
}

func TestPercentile(t *testing.T) {
	sorted := []float64{0, 1, 2, 3, 4}
	if p := percentile(sorted, 0); p != 0 {
		t.Fatalf("p0 = %v, want 0", p)
	}
	if p := percentile(sorted, 1); p != 4 {
		t.Fatalf("p100 = %v, want 4", p)
	}
	if p := percentile(sorted, 0.5); p != 2 {
		t.Fatalf("p50 = %v, want 2", p)
	}
}

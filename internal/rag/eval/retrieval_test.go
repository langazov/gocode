package eval

import (
	"context"
	"math"
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

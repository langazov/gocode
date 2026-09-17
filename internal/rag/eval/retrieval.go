package eval

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
)

// Hit is the subset of rag.SearchHit retrieval scoring needs: enough to
// decide whether a result overlaps a gold Region. Kept narrow (rather than
// depending on package rag directly) so this package has no dependency on
// rag/store/embed and stays testable with a hand-built SearchFunc.
type Hit struct {
	Path      string
	StartLine int
	EndLine   int
}

func (h Hit) region() Region { return Region{Path: h.Path, StartLine: h.StartLine, EndLine: h.EndLine} }

// SearchFunc runs one query and returns up to k ranked hits, most similar
// first — the shape rag.Searcher.Search already has.
type SearchFunc func(ctx context.Context, query string, k int) ([]Hit, error)

// QueryScore is one gold pair's outcome.
type QueryScore struct {
	Gold GoldPair
	// Rank is the 1-based position of the first hit that overlapped any of
	// Gold.Relevant, or 0 if none of the top-K hits did.
	Rank int
	// NDCG is this query's normalized discounted cumulative gain at K —
	// see ndcgAtK's doc comment. Unlike Rank (which only ever asks "was the
	// first relevant hit found, and how fast"), this also rewards finding
	// more than one of several relevant regions (a commit can touch up to
	// MaxFilesPerCommit files), each discounted by how far down the
	// ranking it took to find it.
	NDCG float64
}

func (q QueryScore) hit() bool { return q.Rank > 0 }

func (q QueryScore) reciprocalRank() float64 {
	if q.Rank == 0 {
		return 0
	}
	return 1 / float64(q.Rank)
}

// Stat is a point estimate with a percentile bootstrap 95% confidence
// interval, computed by resampling the per-query scores with replacement.
// The interval is what turns "0.62 vs 0.58" into either "a real improvement"
// or "indistinguishable from noise at this sample size."
type Stat struct {
	Mean   float64
	CILow  float64
	CIHigh float64
}

// Metrics summarizes retrieval quality across a gold set.
type Metrics struct {
	N int // gold pairs scored
	K int // results requested per query

	// RecallAtK is the fraction of queries where at least one relevant
	// region appeared in the top K results.
	RecallAtK Stat
	// MRR is the mean reciprocal rank of the first relevant result (0 for a
	// query with no relevant result in the top K).
	MRR Stat
	// NDCG is the mean normalized discounted cumulative gain at K (see
	// ndcgAtK). Where MRR only asks "how fast was the first relevant hit,"
	// NDCG also credits finding additional relevant regions further down
	// the ranking — a gentler, more forgiving decay by rank than MRR's raw
	// 1/rank (NDCG@2 for a rank-2 hit is ~0.63 vs MRR's 0.5), so the two
	// together show whether a given Recall@K number reflects hits clustered
	// near the top or scattered deep in the results.
	NDCG Stat
}

// defaultBootstrapIterations balances a tight-enough CI against runtime: at
// a few hundred queries, 2000 resamples resolves the interval to about the
// same precision as the sample size itself allows the estimate.
const defaultBootstrapIterations = 2000

// Evaluate runs search once per gold pair and scores the ranked results
// against each pair's relevant regions.
func Evaluate(ctx context.Context, gold []GoldPair, search SearchFunc, k, bootstrapIterations int) (Metrics, []QueryScore, error) {
	if len(gold) == 0 {
		return Metrics{}, nil, fmt.Errorf("eval: empty gold set")
	}
	if bootstrapIterations <= 0 {
		bootstrapIterations = defaultBootstrapIterations
	}

	scores := make([]QueryScore, len(gold))
	for i, g := range gold {
		hits, err := search(ctx, g.Query, k)
		if err != nil {
			return Metrics{}, nil, fmt.Errorf("eval: search %q: %w", g.Query, err)
		}
		scores[i] = QueryScore{Gold: g, Rank: firstRelevantRank(g, hits), NDCG: ndcgAtK(g, hits, k)}
	}

	recall := make([]float64, len(scores))
	rr := make([]float64, len(scores))
	ndcg := make([]float64, len(scores))
	for i, s := range scores {
		if s.hit() {
			recall[i] = 1
		}
		rr[i] = s.reciprocalRank()
		ndcg[i] = s.NDCG
	}

	return Metrics{
		N:         len(gold),
		K:         k,
		RecallAtK: bootstrap(recall, bootstrapIterations),
		MRR:       bootstrap(rr, bootstrapIterations),
		NDCG:      bootstrap(ndcg, bootstrapIterations),
	}, scores, nil
}

func firstRelevantRank(g GoldPair, hits []Hit) int {
	for i, h := range hits {
		region := h.region()
		for _, rel := range g.Relevant {
			if region.Overlaps(rel) {
				return i + 1
			}
		}
	}
	return 0
}

// ndcgAtK is one query's normalized discounted cumulative gain at k:
// DCG@k / IDCG@k, using binary relevance (a hit either overlaps a gold
// region or it doesn't — git-mined pairs carry no finer-grained judgment)
// and treating each of g.Relevant's regions as one distinct relevant item,
// the same unit Recall@K and MRR already score against.
//
// Each relevant region contributes gain exactly once, to the first
// (highest-ranked) hit that overlaps it — not to every hit that happens to
// overlap it. Without that, two adjacent chunks both overlapping the same
// single relevant region (routine with a nonzero chunk overlap) would earn
// gain twice for one real answer, and NDCG could exceed 1.0, breaking the
// normalization the "N" is for. IDCG@k is the gain of the best possible
// ranking: min(len(g.Relevant), k) relevant items placed at ranks 1..m, so
// a query with only one relevant region is capped at the same ceiling a
// single hit at rank 1 already reaches (IDCG=1, same as MRR's).
func ndcgAtK(g GoldPair, hits []Hit, k int) float64 {
	if len(g.Relevant) == 0 {
		return 0
	}
	if len(hits) > k {
		hits = hits[:k]
	}

	credited := make([]bool, len(g.Relevant))
	dcg := 0.0
	for i, h := range hits {
		region := h.region()
		for ri, rel := range g.Relevant {
			if credited[ri] {
				continue
			}
			if region.Overlaps(rel) {
				credited[ri] = true
				dcg += 1 / math.Log2(float64(i+2)) // i is 0-based; rank i+1, discount log2(rank+1)
				break
			}
		}
	}

	ideal := len(g.Relevant)
	if ideal > k {
		ideal = k
	}
	idcg := 0.0
	for i := 0; i < ideal; i++ {
		idcg += 1 / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// bootstrap resamples values with replacement `iterations` times, computing
// each resample's mean, and returns the observed mean plus the 2.5th/97.5th
// percentiles of that resampled distribution as a 95% CI. This is the
// standard percentile bootstrap: it assumes nothing about the shape of the
// per-query score distribution, which a 0/1 recall score never is normal.
func bootstrap(values []float64, iterations int) Stat {
	n := len(values)
	if n == 0 {
		return Stat{}
	}
	observed := mean(values)
	if n == 1 {
		return Stat{Mean: observed, CILow: observed, CIHigh: observed}
	}

	means := make([]float64, iterations)
	resample := make([]float64, n)
	for it := 0; it < iterations; it++ {
		for i := range resample {
			resample[i] = values[rand.IntN(n)]
		}
		means[it] = mean(resample)
	}
	sort.Float64s(means)
	return Stat{
		Mean:   observed,
		CILow:  percentile(means, 0.025),
		CIHigh: percentile(means, 0.975),
	}
}

func mean(values []float64) float64 {
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// percentile linearly interpolates the p-th percentile (p in [0,1]) of an
// already-sorted slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := p * float64(len(sorted)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi {
		return sorted[lo]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

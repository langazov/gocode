package tui

// Stats aggregate: the computation half of the /stats panel. The rendering
// half lives in dialogs_confirm.go (statsBody and friends), driven through
// the dialog package's note field; what remains here is the aggregation the
// panel renders — per-session, per-model and aggregate token usage, cost,
// and prompt-cache hit rates, grouped by model because that is the dimension
// that answers "how much is this model costing me?".

import (
	"fmt"
	"sort"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// Panel geometry. statsPad is the left inset shared with statusOverlay and
// helpOverlay (dialogHeader's `pad` argument for a non-select dialog);
// statsLabelWidth is the key column every statsRow aligns its value against,
// sized to the longest label below ("Total Messages").
const (
	statsPad        = 2
	statsLabelWidth = 16
	statsIndent     = 2
)

// percent formats a cache hit rate. One control, so the aggregate, per-model
// and per-session figures cannot drift apart in precision.
func statsPercent(rate float64) string { return fmt.Sprintf("%.2f%%", rate) }

// --- aggregate types --------------------------------------------------------

type statsAggregate struct {
	sessionCount int
	// loaded / pending / unavailable partition sessionCount by what the
	// stats batch actually knows about each session, so the panel can tell
	// "still fetching" from "the server has nothing" instead of rendering
	// both as zeros. loadAllStats swallows per-session errors (a missing
	// endpoint for one session must not blank the whole overlay), so a
	// session absent from a batch that HAS landed is unavailable, and one
	// absent because no batch has landed yet is pending.
	loaded      int
	pending     int
	unavailable int
	messages    int
	cost        float64
	input       int
	output      int
	reasoning   int
	cacheRead   int
	cacheWrite  int
	models      []modelStats
	sessions    []sessionStats
}

type modelStats struct {
	providerID   string
	modelID      string
	sessionCount int
	cost         float64
	input        int
	output       int
	cacheRead    int
	hitRate      float64
}

type sessionStats struct {
	sessionID string
	title     string
	// resolved reports whether stats for this session arrived; pendingLabel
	// is what the breakdown says in their place when they did not.
	resolved     bool
	pendingLabel string
	// updated / created are the session's own timestamps, carried so the
	// breakdown can be ordered by recency. updated is the last activity and
	// is what "latest" means here; created is the tie-break for a session
	// the server has never stamped an update on.
	updated    int64
	created    int64
	providerID string
	modelID    string
	cost       float64
	input      int
	output     int
	cacheRead  int
	messages   int
	hitRate    float64
}

// modelUsage is the sort key for the By Model section: every token attributed
// to the model.
func modelUsage(m modelStats) int { return m.input + m.output + m.cacheRead }

// sessionRecency is the sort key for the By Session section: last activity,
// falling back to creation for a session with no recorded update.
func sessionRecency(s sessionStats) int64 {
	if s.updated > 0 {
		return s.updated
	}
	return s.created
}

// computeStatsAggregate walks the loaded sessions + allStats (and the active
// session's own stats as a fallback) and produces the aggregate the overlay
// renders.
func (a *App) computeStatsAggregate() statsAggregate {
	agg := statsAggregate{sessionCount: len(a.sessions)}

	// Build a set of (providerID, modelID) -> per-model accumulator.
	type modelKey struct {
		providerID, modelID string
	}
	modelAccum := map[modelKey]*modelStats{}
	sessionList := make([]sessionStats, 0, len(a.sessions))

	for _, s := range a.sessions {
		// Resolve stats: prefer the allStats batch, fall back to the active
		// session's own stats (which may have arrived before allStats).
		var st *client.Stats
		if a.allStats != nil {
			st = a.allStats[s.ID]
		}
		if st == nil && a.active != nil && s.ID == a.active.ID {
			st = a.stats
		}
		if st == nil {
			// No stats for this session. Which of the two reasons applies
			// decides both the counter and what its row says: a batch that
			// has not landed yet is still coming, one that has landed
			// without this session is not.
			ss := sessionStats{
				sessionID: s.ID,
				title:     s.Title,
				updated:   s.TimeUpdated,
				created:   s.TimeCreated,
			}
			if a.allStats == nil {
				agg.pending++
				ss.pendingLabel = "loading…"
			} else {
				agg.unavailable++
				ss.pendingLabel = "no stats reported"
			}
			if s.Model != nil {
				ss.providerID = s.Model.ProviderID
				ss.modelID = s.Model.ID
			}
			sessionList = append(sessionList, ss)
			continue
		}
		agg.loaded++

		agg.cost += st.Cost
		agg.input += st.TokensInput
		agg.output += st.TokensOutput
		agg.reasoning += st.TokensReasoning
		agg.cacheRead += st.TokensCacheRead
		agg.cacheWrite += st.TokensCacheWrite
		agg.messages += st.Messages

		var providerID, modelID string
		if s.Model != nil {
			providerID = s.Model.ProviderID
			modelID = s.Model.ID
		}

		prompt := st.TokensInput + st.TokensCacheRead
		var hitRate float64
		if prompt > 0 {
			hitRate = float64(st.TokensCacheRead) / float64(prompt) * 100
		}

		ss := sessionStats{
			sessionID:  s.ID,
			title:      s.Title,
			resolved:   true,
			updated:    s.TimeUpdated,
			created:    s.TimeCreated,
			providerID: providerID,
			modelID:    modelID,
			cost:       st.Cost,
			input:      st.TokensInput,
			output:     st.TokensOutput,
			cacheRead:  st.TokensCacheRead,
			messages:   st.Messages,
			hitRate:    hitRate,
		}
		sessionList = append(sessionList, ss)

		key := modelKey{providerID, modelID}
		m, ok := modelAccum[key]
		if !ok {
			m = &modelStats{providerID: providerID, modelID: modelID}
			modelAccum[key] = m
		}
		m.sessionCount++
		m.cost += st.Cost
		m.input += st.TokensInput
		m.output += st.TokensOutput
		m.cacheRead += st.TokensCacheRead
	}

	// Finalize model hit rates.
	for _, m := range modelAccum {
		prompt := m.input + m.cacheRead
		if prompt > 0 {
			m.hitRate = float64(m.cacheRead) / float64(prompt) * 100
		} else {
			m.hitRate = 0
		}
	}

	// Models are ordered by usage, heaviest first — the question this
	// section answers is "where is the work (and the money) going", and the
	// answer belongs at the top. Usage is every token attributed to the
	// model, not just the prompt side: an expensive model earning its place
	// on output alone should not sort below a cheap one with a large cached
	// prompt.
	agg.models = make([]modelStats, 0, len(modelAccum))
	for _, m := range modelAccum {
		agg.models = append(agg.models, *m)
	}
	sort.Slice(agg.models, func(i, j int) bool {
		a, b := agg.models[i], agg.models[j]
		if ua, ub := modelUsage(a), modelUsage(b); ua != ub {
			return ua > ub
		}
		// Deterministic tie-breaks, so a model with no usage yet does not
		// shuffle between renders.
		if a.cost != b.cost {
			return a.cost > b.cost
		}
		return a.providerID+"/"+a.modelID < b.providerID+"/"+b.modelID
	})

	// Sessions are ordered by recency, latest first — a session list is read
	// as "what have I been doing", so the one just worked in belongs at the
	// top. TimeUpdated is the last activity; TimeCreated is the fallback for
	// a session the server has never stamped an update on, and the ID breaks
	// a remaining tie so the order is stable across renders.
	sort.Slice(sessionList, func(i, j int) bool {
		a, b := sessionList[i], sessionList[j]
		if at, bt := sessionRecency(a), sessionRecency(b); at != bt {
			return at > bt
		}
		return a.sessionID > b.sessionID
	})
	agg.sessions = sessionList

	return agg
}

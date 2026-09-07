package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The footer's throughput counter: how fast model output is arriving, averaged
// over the last ten seconds.
//
// The shape is a ring of one-second buckets. Every second the bytes that
// arrived since the previous tick are converted to tokens and written to the
// next slot, overwriting whatever was there ten seconds ago; the rate on
// screen is the sum of the ring over its length. Averaging is the point — a
// raw per-second count swings between zero and several hundred depending on
// where a provider happens to flush, which is unreadable in a status bar. Ten
// seconds is long enough to steady it and short enough that the number still
// tracks what is happening now.

const (
	// tpsWindowSeconds is both the ring's length and its divisor, so a
	// second in which nothing arrived counts as a zero rather than being
	// skipped: a stall has to drag the average down, or a counter frozen at
	// its last good value would read as throughput that is not happening.
	tpsWindowSeconds = 10

	// tpsBytesPerToken is the same rough conversion internal/session's
	// estimateTokens uses. Nothing on the wire carries a token count while a
	// step is streaming — the exact figure arrives with the step's usage
	// record, seconds later and all at once — so a live counter has to
	// estimate. See tpsMeter.rate for what that costs.
	tpsBytesPerToken = 4
)

// tpsMeter is the ring and the cursor into it.
type tpsMeter struct {
	ring [tpsWindowSeconds]int
	at   int
	// lastBytes is the cumulative counter as of the previous sample, which is
	// what makes each slot a difference rather than a total.
	lastBytes int
}

// sample folds one second of traffic into the window, given the aggregator's
// cumulative byte count.
func (m *tpsMeter) sample(cumulativeBytes int) {
	delta := cumulativeBytes - m.lastBytes
	if delta < 0 {
		// The counter only ever grows, so this means the aggregator behind it
		// was replaced. Treat the second as empty rather than recording a
		// negative slot.
		delta = 0
	}
	m.lastBytes = cumulativeBytes
	m.ring[m.at] = (delta + tpsBytesPerToken - 1) / tpsBytesPerToken
	m.at = (m.at + 1) % tpsWindowSeconds
}

// rebase moves the baseline to the current count without recording a slot, so
// that arming the meter after an idle stretch does not book everything since
// the last sample into one second.
func (m *tpsMeter) rebase(cumulativeBytes int) {
	m.lastBytes = cumulativeBytes
}

// rate is the window average: every slot summed, over the number of slots.
//
// It is an estimate twice over — bytes are converted to tokens at a fixed
// ratio, and a partial second at either end of the window is counted whole —
// so it is worth reading as "roughly this fast", not as a measurement to
// reconcile against a bill. The usage segment beside it carries the exact
// counts the provider reported.
func (m *tpsMeter) rate() float64 {
	total := 0
	for _, slot := range m.ring {
		total += slot
	}
	return float64(total) / tpsWindowSeconds
}

// idle reports whether the window has emptied out — nothing arrived in the
// last ten seconds, so there is no rate left to show and no reason to keep
// ticking.
func (m *tpsMeter) idle() bool {
	for _, slot := range m.ring {
		if slot != 0 {
			return false
		}
	}
	return true
}

type tpsTickMsg struct{}

// startTPS keeps exactly one one-second sampling loop alive, on the same
// guard-and-restart pattern as startSpinner (which calls this, so that every
// place a turn can start also starts the meter).
//
// The loop outlives the turn on purpose. Sampling stops only once the window
// has emptied, which lets the rate fall away over ten seconds instead of
// freezing at whatever it read when the last token landed.
func (a *App) startTPS() tea.Cmd {
	if a.tpsTicking || (!a.busy && a.tps.idle()) {
		return nil
	}
	if a.tps.idle() {
		// Arming from cold: everything the counter accumulated while the loop
		// was stopped predates this window.
		a.tps.rebase(a.agents.ReceivedBytes)
	}
	a.tpsTicking = true
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tpsTickMsg{} })
}

// footerTPS renders the counter, or "" when there is nothing to report — an
// idle session should not carry a permanent "0.0 tok/s".
func (a *App) footerTPS() string {
	rate := a.tps.rate()
	if rate <= 0 {
		return ""
	}
	return a.styles().Muted.Render(formatTPS(rate))
}

// formatTPS drops the decimal once the number is big enough that a tenth of a
// token per second is noise, so the segment's width stays stable.
func formatTPS(rate float64) string {
	if rate >= 100 {
		return fmt.Sprintf("%.0f tok/s", rate)
	}
	return fmt.Sprintf("%.1f tok/s", rate)
}

package tui

import (
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// Each sample records the traffic since the previous one, not the running
// total: the aggregator's counter only ever grows.
func TestTPSMeterSamplesDifferences(t *testing.T) {
	var m tpsMeter
	m.sample(400)  // 400 bytes → 100 tokens
	m.sample(800)  // another 400 → 100
	m.sample(1200) // another 400 → 100
	if got := m.rate(); got != 30 {
		t.Fatalf("rate = %v, want 300 tokens over a 10-slot window = 30", got)
	}
}

// The window is the sum of its slots over the number of slots, so an empty
// second drags the average down instead of being skipped.
func TestTPSMeterAveragesOverTheWholeWindow(t *testing.T) {
	var m tpsMeter
	// One busy second, then nine idle ones.
	m.sample(4000) // 1000 tokens
	for i := 0; i < 9; i++ {
		m.sample(4000)
	}
	if got := m.rate(); got != 100 {
		t.Fatalf("rate = %v, want 1000/10 = 100", got)
	}
}

// Slot eleven overwrites slot one: the window only ever covers the last ten
// seconds.
func TestTPSMeterRingWraps(t *testing.T) {
	var m tpsMeter
	total := 0
	for i := 0; i < tpsWindowSeconds; i++ {
		total += 4000
		m.sample(total) // 1000 tokens a second
	}
	if got := m.rate(); got != 1000 {
		t.Fatalf("rate = %v, want a full window of 1000/s", got)
	}
	// Ten quiet seconds later the window has emptied completely.
	for i := 0; i < tpsWindowSeconds; i++ {
		m.sample(total)
	}
	if got := m.rate(); got != 0 {
		t.Fatalf("rate = %v, want the window to have emptied", got)
	}
	if !m.idle() {
		t.Error("an emptied window is idle")
	}
}

func TestTPSMeterIgnoresACounterReset(t *testing.T) {
	var m tpsMeter
	m.sample(4000)
	m.sample(0) // the aggregator behind it was replaced
	if got := m.rate(); got != 100 {
		t.Fatalf("rate = %v, want the reset second to count as empty", got)
	}
}

// Arming after an idle stretch must not book everything accumulated in the
// meantime into a single second.
func TestTPSMeterRebase(t *testing.T) {
	var m tpsMeter
	m.rebase(1_000_000)
	m.sample(1_000_400)
	if got := m.rate(); got != 10 {
		t.Fatalf("rate = %v, want only the 400 bytes since the rebase", got)
	}
}

func TestFormatTPS(t *testing.T) {
	for rate, want := range map[float64]string{
		0.4:   "0.4 tok/s",
		12.34: "12.3 tok/s",
		99.9:  "99.9 tok/s",
		100:   "100 tok/s",
		1234:  "1234 tok/s",
	} {
		if got := formatTPS(rate); got != want {
			t.Errorf("formatTPS(%v) = %q, want %q", rate, got, want)
		}
	}
}

// Assistant text and thinking both count, across every session on the stream.
func TestAggregatorCountsReceivedBytes(t *testing.T) {
	state := newTree()
	state.apply(client.Event{
		Type: "session.next.text.delta", Session: "ses_1",
		Data: map[string]any{"assistantMessageID": "msg_1", "delta": "hello"},
	})
	state.apply(client.Event{
		Type: "session.next.reasoning.delta", Session: "ses_1",
		Data: map[string]any{"reasoningID": "msg_1-reasoning", "delta": "think"},
	})
	// A subagent's output is still output the user is waiting on.
	state.apply(client.Event{
		Type: "session.next.text.delta", Session: "ses_child",
		Data: map[string]any{"assistantMessageID": "msg_2", "delta": "!"},
	})
	if got := state.snapshot(0).ReceivedBytes; got != 11 {
		t.Fatalf("ReceivedBytes = %d, want 5+5+1", got)
	}

	// The counter is cumulative: a second snapshot does not reset it.
	state.apply(client.Event{
		Type: "session.next.text.delta", Session: "ses_1",
		Data: map[string]any{"assistantMessageID": "msg_1", "delta": "more"},
	})
	if got := state.snapshot(0).ReceivedBytes; got != 15 {
		t.Fatalf("ReceivedBytes = %d, want the running total", got)
	}
}

// A step settling clears the streamed text buffers; the throughput counter
// must not be cleared with them.
func TestAggregatorKeepsCountAcrossStepEnd(t *testing.T) {
	state := newTree()
	state.apply(client.Event{
		Type: "session.next.text.delta", Session: "ses_1",
		Data: map[string]any{"assistantMessageID": "msg_1", "delta": "hello"},
	})
	state.apply(client.Event{Type: "session.next.step.ended", Session: "ses_1", Data: map[string]any{}})
	if got := state.snapshot(0).ReceivedBytes; got != 5 {
		t.Fatalf("ReceivedBytes = %d, want it to survive the step boundary", got)
	}
}

func TestFooterShowsThroughputWhileRunning(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark")}
	app.tps.sample(4000) // 1000 tokens in one second → 100/s over the window

	segments := app.footerRight(footerWidthPolicy(100))
	joined := plain(strings.Join(segments, " "))
	if !strings.Contains(joined, "100 tok/s") {
		t.Fatalf("footer should carry the rate, got %q", joined)
	}
}

// An idle session should not carry a permanent "0.0 tok/s".
func TestFooterHidesThroughputWhenIdle(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark")}
	joined := plain(strings.Join(app.footerRight(footerWidthPolicy(100)), " "))
	if strings.Contains(joined, "tok/s") {
		t.Fatalf("an idle footer should have no rate, got %q", joined)
	}
}

// The rate sits under the same width gate as the usage meter beside it.
func TestFooterDropsThroughputWhenNarrow(t *testing.T) {
	app := &App{width: 60, height: 30, theme: themeResolve("gocode-dark")}
	app.tps.sample(4000)
	joined := plain(strings.Join(app.footerRight(footerWidthPolicy(60)), " "))
	if strings.Contains(joined, "tok/s") {
		t.Fatalf("a narrow footer should drop the rate, got %q", joined)
	}
}

// Exactly one sampling loop stays in flight, and it outlives the turn so the
// rate can decay instead of freezing at its last reading.
func TestStartTPSKeepsOneLoop(t *testing.T) {
	app := &App{busy: true}
	if cmd := app.startTPS(); cmd == nil {
		t.Fatal("a running turn should start the loop")
	}
	if cmd := app.startTPS(); cmd != nil {
		t.Fatal("a second loop must not start alongside the first")
	}

	// The turn ends with traffic still in the window: sampling continues.
	app.tpsTicking, app.busy = false, false
	app.tps.sample(4000)
	if cmd := app.startTPS(); cmd == nil {
		t.Fatal("the loop should outlive the turn while the window has traffic")
	}

	// Once the window empties there is nothing left to show.
	app.tpsTicking = false
	app.tps = tpsMeter{}
	if cmd := app.startTPS(); cmd != nil {
		t.Fatal("an idle, settled session should stop sampling")
	}
}

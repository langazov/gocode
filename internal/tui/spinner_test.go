package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/anomalyco/opencode-go/internal/tui/client"
)

// --- the tick loop ---------------------------------------------------------

// The regression this guards: applySnapshot is where a live turn's busy state
// actually lands (the aggregator is the only thing watching events), and it
// used to set a.busy without starting a tick loop. messagesMsg only starts one
// on a false->true transition, which the snapshot had already consumed — so
// the footer spinner sat frozen on frame 0 for the whole turn.
func TestSnapshotStartsTheSpinnerLoop(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.active = &client.Session{ID: "ses_1"}

	node := newSessionNode("ses_1")
	node.Busy = true
	cmd := app.Update(snapshotMsg{snapshot: Snapshot{Sessions: map[string]*SessionNode{"ses_1": node}}})

	if !app.busy {
		t.Fatal("a busy node should mark the app busy")
	}
	if !app.spinning {
		t.Fatal("a busy snapshot must start the spinner tick loop")
	}
	if cmd == nil {
		t.Fatal("a busy snapshot must return the tick command")
	}
}

// Every busy-setting path calls startSpinner, so it has to be idempotent:
// without the guard each caller starts its own loop and the frames advance
// once per loop per tick.
func TestStartSpinnerDoesNotStackLoops(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.busy = true

	if cmd := app.startSpinner(); cmd == nil {
		t.Fatal("the first call should start a loop")
	}
	if cmd := app.startSpinner(); cmd != nil {
		t.Fatal("a second call while a loop is in flight must not start another")
	}

	// The tick hands the loop back, so the next call may start one again.
	app.Update(spinnerTickMsg{})
	if !app.spinning {
		t.Fatal("a tick while busy should immediately re-arm the loop")
	}
}

func TestSpinnerLoopStopsWhenIdle(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.busy = true
	app.startSpinner()

	app.busy = false
	if cmd := app.Update(spinnerTickMsg{}); cmd != nil {
		t.Fatal("an idle tick should end the loop")
	}
	if app.spinning {
		t.Fatal("an idle tick should clear the in-flight flag")
	}
}

func TestBrailleSpinnerRunsAtHalfTheTickRate(t *testing.T) {
	// <Spinner> is interval={80} and the shared loop is the scanner's 40ms.
	app := &App{animationsEnabled: true}
	app.spinnerFrame = 0
	first := app.spinnerGlyph()
	app.spinnerFrame = 1
	if app.spinnerGlyph() != first {
		t.Fatal("the braille frame should hold across two 40ms ticks")
	}
	app.spinnerFrame = 2
	if app.spinnerGlyph() == first {
		t.Fatal("the braille frame should advance on the second tick")
	}
}

// --- the Knight Rider scanner (ui/spinner.ts) ------------------------------

func TestScannerCycleLengthMatchesUpstream(t *testing.T) {
	// createFrames: width + holdEnd + (width-1) + holdStart.
	if spinnerCycle != 8+9+7+30 {
		t.Fatalf("cycle = %d, want %d", spinnerCycle, 8+9+7+30)
	}
}

func TestScannerStateSweepsForwardThenHoldsThenReturns(t *testing.T) {
	for _, tc := range []struct {
		frame   int
		pos     int
		holding bool
		forward bool
	}{
		{0, 0, false, true},                // start of the forward sweep
		{7, 7, false, true},                // head at the far end
		{8, 7, true, true},                 // holding at the end
		{17, 6, false, false},              // first backward frame
		{23, 0, false, false},              // head back at the start
		{spinnerCycle - 1, 0, true, false}, // the long hold at rest
	} {
		got := spinnerScannerState(tc.frame, spinnerWidth)
		if got.activePosition != tc.pos || got.isHolding != tc.holding || got.isMovingForward != tc.forward {
			t.Fatalf("frame %d: %+v, want pos=%d holding=%v forward=%v",
				tc.frame, got, tc.pos, tc.holding, tc.forward)
		}
	}
}

// The trail runs behind the head, in the direction of travel — the whole point
// of calculateColorIndex's directional distance.
func TestScannerTrailFollowsTheDirectionOfTravel(t *testing.T) {
	forward := spinnerScannerState(5, spinnerWidth) // head at 5, moving right
	if got := spinnerColorIndex(5, spinnerTrailSteps, forward); got != 0 {
		t.Fatalf("the head is index 0, got %d", got)
	}
	if got := spinnerColorIndex(4, spinnerTrailSteps, forward); got != 1 {
		t.Fatalf("the cell behind the head is index 1, got %d", got)
	}
	if got := spinnerColorIndex(6, spinnerTrailSteps, forward); got != -1 {
		t.Fatalf("the cell ahead of the head is inactive, got %d", got)
	}

	backward := spinnerScannerState(19, spinnerWidth) // moving left
	if got := spinnerColorIndex(backward.activePosition+1, spinnerTrailSteps, backward); got != 1 {
		t.Fatalf("moving left, the trail is to the right, got %d", got)
	}
}

func TestScannerFadeBottomsOutAtMinAlpha(t *testing.T) {
	// The end of the long resting hold is the dimmest the bar ever gets.
	fade := spinnerFade(spinnerScannerState(spinnerCycle-1, spinnerWidth))
	if fade < spinnerMinAlpha || fade > spinnerMinAlpha+0.05 {
		t.Fatalf("fade at rest = %v, want ~%v", fade, spinnerMinAlpha)
	}
	// The first frame of a sweep fades in from exactly minAlpha.
	if fade := spinnerFade(spinnerScannerState(0, spinnerWidth)); fade != spinnerMinAlpha {
		t.Fatalf("fade at the start of a sweep = %v, want %v", fade, spinnerMinAlpha)
	}
}

func TestScannerRendersAFullWidthBarThatMoves(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")

	frames := map[string]bool{}
	for i := range spinnerCycle {
		app.spinnerFrame = i
		bar := ansi.Strip(app.scannerSpinner(app.theme.Primary, app.theme.Background))
		if got := len([]rune(bar)); got != spinnerWidth {
			t.Fatalf("frame %d: bar is %d cells, want %d", i, got, spinnerWidth)
		}
		if strings.Trim(bar, spinnerActiveGlyph+spinnerInactiveGlyph) != "" {
			t.Fatalf("frame %d: unexpected glyph in %q", i, bar)
		}
		frames[bar] = true
	}
	if len(frames) < 8 {
		t.Fatalf("the scanner should sweep through distinct frames, got %d", len(frames))
	}
}

func TestScannerFallsBackToTheStaticGlyphWithoutAnimations(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.animationsEnabled = false
	if got := app.scannerSpinner(app.theme.Primary, app.theme.Background); got != "⋯" {
		t.Fatalf("disabled scanner = %q, want the static ⋯ fallback", got)
	}
}

// The footer is where the scanner is actually used; a frozen bar there is the
// user-visible symptom of every bug above.
func TestFooterSpinnerAdvancesWithTheTickLoop(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 160, 40
	app.active = &client.Session{ID: "ses_1", Directory: "/tmp/p"}
	app.busy = true

	seen := map[string]bool{}
	var cmd tea.Cmd = app.startSpinner()
	for range 8 {
		if cmd == nil {
			t.Fatal("the loop stopped while still busy")
		}
		seen[ansi.Strip(app.chatFooter())] = true
		cmd = app.Update(spinnerTickMsg{})
	}
	if len(seen) < 4 {
		t.Fatalf("the footer spinner should repaint each tick, saw %d distinct rows", len(seen))
	}
}

// --- interrupt ends the animation ------------------------------------------

// projectStepFailed — the settlement for an interrupted or provider-failed
// turn — records `error` and `time.completed` but never a `finish` reason.
// Keying "still running" on `finish` alone meant an interrupted turn read as
// unfinished forever.
func TestSettledAssistantMessagesAreNotRunning(t *testing.T) {
	for _, tc := range []struct {
		name string
		data map[string]any
		want bool
	}{
		{
			name: "interrupted: error and a completion time, no finish",
			data: map[string]any{
				"error": map[string]any{"type": "unknown", "message": "context canceled"},
				"time":  map[string]any{"created": 1, "completed": 2},
			},
		},
		{
			name: "provider failure settled with only a completion time",
			data: map[string]any{"time": map[string]any{"created": 1, "completed": 2}},
		},
		{
			name: "normal settlement",
			data: map[string]any{"finish": "stop", "time": map[string]any{"created": 1, "completed": 2}},
		},
		{
			name: "still streaming",
			data: map[string]any{"time": map[string]any{"created": 1}},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.data)
			if err != nil {
				t.Fatal(err)
			}
			got := hasUnfinishedAssistant([]client.Message{{ID: "m1", Type: "assistant", Data: raw}})
			if got != tc.want {
				t.Fatalf("hasUnfinishedAssistant = %v, want %v", got, tc.want)
			}
		})
	}
}

// End to end over the exact sequence a double-escape produces: the aggregator
// clears busy and marks the session dirty, the dirty snapshot refetches the
// messages, and the refetch must not turn busy — and the spinner — back on.
func TestInterruptStopsTheFooterAnimation(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 160, 40
	app.active = &client.Session{ID: "ses_1", Directory: "/tmp/p"}

	// A turn is running: the spinner loop is live.
	running := newSessionNode("ses_1")
	running.Busy = true
	app.Update(snapshotMsg{snapshot: Snapshot{Sessions: map[string]*SessionNode{"ses_1": running}}})
	if !app.busy || !app.spinning {
		t.Fatal("a busy snapshot should be animating")
	}

	// session.next.step.failed lands: the aggregator clears Busy and marks the
	// session dirty, which drives a message refetch.
	settled := newSessionNode("ses_1")
	settled.Busy = false
	app.Update(snapshotMsg{snapshot: Snapshot{
		Sessions: map[string]*SessionNode{"ses_1": settled},
		Dirty:    map[string]bool{"ses_1": true},
	}})
	if app.busy {
		t.Fatal("a settled snapshot should clear busy")
	}

	// The refetch returns the interrupted message projectStepFailed wrote.
	interrupted, err := json.Marshal(map[string]any{
		"model":   map[string]string{"providerID": "anthropic", "id": "claude"},
		"content": []map[string]any{{"type": "text", "text": "partial"}},
		"error":   map[string]any{"type": "unknown", "message": "context canceled"},
		"time":    map[string]any{"created": 1, "completed": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Update(messagesMsg{
		sessionID: "ses_1",
		messages:  []client.Message{{ID: "m1", Type: "assistant", Data: interrupted}},
	})
	if app.busy {
		t.Fatal("refetching an interrupted turn must not restart it")
	}

	// The in-flight tick now finds an idle app and ends the loop for good.
	if cmd := app.Update(spinnerTickMsg{}); cmd != nil {
		t.Fatal("the spinner loop should end after an interrupt")
	}
	if app.spinning {
		t.Fatal("the loop should not be re-armed after an interrupt")
	}
	footer := ansi.Strip(app.chatFooter())
	if strings.Contains(footer, spinnerActiveGlyph) || strings.Contains(footer, "esc interrupt") {
		t.Fatalf("an idle footer should show neither the scanner nor the interrupt hint, got %q", footer)
	}
	if !strings.Contains(footer, "interrupted") {
		t.Fatalf("the footer should report that the turn was interrupted, got %q", footer)
	}
}

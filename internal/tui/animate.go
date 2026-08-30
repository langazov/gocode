package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// This file ports packages/tui/src/util/signal.ts's two Solid helpers to
// Bubble Tea's message-driven model. Solid re-runs an effect automatically
// whenever a tracked signal changes; Bubble Tea has no such tracking, so
// callers must explicitly re-sync (fadeAnim) or re-Set (debouncer) at the
// points where the underlying value can change, and the animation timers
// signal.ts drives with setInterval/setTimeout become tea.Tick commands
// carrying a generation number in place of a cancellable timer handle.

// fadeAnimStep and fadeAnimDuration mirror createFadeIn's 16ms interval over
// a 160ms cubic-smoothstep ease.
const (
	fadeAnimStep     = 16 * time.Millisecond
	fadeAnimDuration = 160 * time.Millisecond
)

// fadeAnim ports createFadeIn(show, enabled): an alpha that jumps straight to
// 1 the first time it is enabled-but-already-revealed (or disabled), and
// animates 0->1 over fadeAnimDuration the first time show flips true while
// animations are enabled. Once revealed, later shows never re-animate,
// matching the TS closure's `revealed` flag.
type fadeAnim struct {
	alpha     float64
	revealed  bool
	animating bool
	start     time.Time
	gen       int
}

// newFadeAnim mirrors `useSignal(show() ? 1 : 0)` plus `let revealed = show()`.
func newFadeAnim(show bool) *fadeAnim {
	f := &fadeAnim{revealed: show}
	if show {
		f.alpha = 1
	}
	return f
}

// Alpha returns the current 0..1 value, ready for theme.FadeColor/Tint.
func (f *fadeAnim) Alpha() float64 { return f.alpha }

// fadeTickMsg drives one animation step; gen guards against a stale tick from
// a superseded animation still firing after a newer sync (Solid's onCleanup
// clearInterval, replicated by ignoring ticks whose generation is behind).
type fadeTickMsg struct {
	anim *fadeAnim
	gen  int
}

// Sync ports the `on([show, enabled], ...)` effect body: call it whenever show
// or enabled may have changed (a catalog load resolving, a mode switching).
func (f *fadeAnim) Sync(show, enabled bool) tea.Cmd {
	if !show {
		f.alpha = 0
		f.animating = false
		f.gen++
		return nil
	}
	if !enabled || f.revealed {
		f.revealed = true
		f.alpha = 1
		f.animating = false
		f.gen++
		return nil
	}
	f.revealed = true
	f.alpha = 0
	f.start = time.Now()
	f.animating = true
	f.gen++
	return f.tick(f.gen)
}

func (f *fadeAnim) tick(gen int) tea.Cmd {
	return tea.Tick(fadeAnimStep, func(time.Time) tea.Msg {
		return fadeTickMsg{anim: f, gen: gen}
	})
}

// Advance applies one fadeTickMsg, mirroring the setInterval body: it is a
// no-op if a newer Sync superseded this animation. Returns a Cmd to schedule
// the next tick, or nil once progress reaches 1 (clearInterval).
func (f *fadeAnim) Advance(msg fadeTickMsg) tea.Cmd {
	if msg.anim != f || msg.gen != f.gen || !f.animating {
		return nil
	}
	progress := float64(time.Since(f.start)) / float64(fadeAnimDuration)
	if progress >= 1 {
		progress = 1
		f.animating = false
	}
	f.alpha = progress * progress * (3 - 2*progress)
	if !f.animating {
		return nil
	}
	return f.tick(f.gen)
}

// debouncer ports createDebouncedSignal(value, ms): repeated Set calls
// collapse into whichever value is still pending once ms elapses with no
// further Set call. Bubble Tea has no clearTimeout, so a generation counter
// takes its place — Apply ignores any debouncedMsg whose generation the
// debouncer has since moved past.
type debouncer struct {
	value string
	gen   int
}

func newDebouncer(value string) *debouncer {
	return &debouncer{value: value}
}

// Value returns the last value that survived its debounce window.
func (d *debouncer) Value() string { return d.value }

type debouncedMsg struct {
	target *debouncer
	gen    int
	value  string
}

// Set schedules value to become Value() after delay elapses with no further
// Set call, exactly like signal.ts's debounced setter (clearTimeout +
// setTimeout).
func (d *debouncer) Set(value string, delay time.Duration) tea.Cmd {
	d.gen++
	gen := d.gen
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return debouncedMsg{target: d, gen: gen, value: value}
	})
}

// Apply commits msg if no newer Set has superseded it, reporting whether
// Value() changed.
func (d *debouncer) Apply(msg debouncedMsg) bool {
	if msg.target != d || msg.gen != d.gen {
		return false
	}
	d.value = msg.value
	return true
}

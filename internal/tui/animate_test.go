package tui

import (
	"testing"
	"time"
)

// --- fadeAnim: ports createFadeIn's semantics from util/signal.ts ---------

func TestFadeAnimInitialState(t *testing.T) {
	if got := newFadeAnim(false).Alpha(); got != 0 {
		t.Fatalf("newFadeAnim(false).Alpha() = %v, want 0", got)
	}
	hidden := newFadeAnim(false)
	if hidden.revealed {
		t.Fatalf("newFadeAnim(false).revealed = true, want false")
	}
	shown := newFadeAnim(true)
	if got := shown.Alpha(); got != 1 {
		t.Fatalf("newFadeAnim(true).Alpha() = %v, want 1", got)
	}
	if !shown.revealed {
		t.Fatalf("newFadeAnim(true).revealed = false, want true")
	}
}

func TestFadeAnimSyncHidingResetsAlphaToZero(t *testing.T) {
	f := newFadeAnim(true)
	if cmd := f.Sync(false, true); cmd != nil {
		t.Fatalf("Sync(false, true) returned a Cmd, want nil (no interval while hidden)")
	}
	if f.Alpha() != 0 {
		t.Fatalf("Alpha() = %v after hiding, want 0", f.Alpha())
	}
}

// mirrors: revealed already true (or animations disabled) => snap to 1, no
// setInterval.
func TestFadeAnimSyncRevealedOrDisabledSnapsWithoutAnimating(t *testing.T) {
	f := newFadeAnim(false)
	cmd := f.Sync(true, false) // enabled=false: TS takes the `!animate` branch
	if cmd != nil {
		t.Fatalf("Sync(true, false) returned a Cmd, want nil (snaps instantly)")
	}
	if f.Alpha() != 1 {
		t.Fatalf("Alpha() = %v, want 1", f.Alpha())
	}
	if !f.revealed {
		t.Fatalf("revealed = false after a shown sync, want true")
	}

	// Once revealed, a later show (even with animations enabled) never
	// re-animates — this is the TS closure's one-shot `revealed` latch.
	f.Sync(false, true) // hide first (revealed persists per TS: never reset)
	cmd = f.Sync(true, true)
	if cmd != nil {
		t.Fatalf("Sync after already revealed returned a Cmd, want nil (revealed latch)")
	}
	if f.Alpha() != 1 {
		t.Fatalf("Alpha() = %v after re-showing a revealed fade, want 1", f.Alpha())
	}
}

// mirrors: first transition to visible while enabled => alpha starts at 0 and
// animates via the 16ms interval up to 1 over 160ms with cubic smoothstep
// easing (progress*progress*(3-2*progress)).
func TestFadeAnimAnimatesOnFirstReveal(t *testing.T) {
	f := newFadeAnim(false)
	cmd := f.Sync(true, true)
	if cmd == nil {
		t.Fatalf("Sync(true, true) on first reveal returned nil, want a tick Cmd")
	}
	if f.Alpha() != 0 {
		t.Fatalf("Alpha() = %v immediately after starting the animation, want 0", f.Alpha())
	}
	if !f.animating {
		t.Fatalf("animating = false right after Sync started the animation")
	}

	// Halfway through the 160ms window: smoothstep(0.5) == 0.5 exactly.
	f.start = time.Now().Add(-fadeAnimDuration / 2)
	cmd = f.Advance(fadeTickMsg{anim: f, gen: f.gen})
	if cmd == nil {
		t.Fatalf("Advance at the halfway point returned nil, want a Cmd for the next tick")
	}
	if diff := f.Alpha() - 0.5; diff > 1e-3 || diff < -1e-3 {
		t.Fatalf("Alpha() at progress=0.5 = %v, want ~0.5", f.Alpha())
	}

	// Past the window: clamps to 1 and stops (clearInterval).
	f.start = time.Now().Add(-2 * fadeAnimDuration)
	cmd = f.Advance(fadeTickMsg{anim: f, gen: f.gen})
	if cmd != nil {
		t.Fatalf("Advance past the animation window returned a Cmd, want nil")
	}
	if f.Alpha() != 1 {
		t.Fatalf("Alpha() after the animation completes = %v, want 1", f.Alpha())
	}
	if f.animating {
		t.Fatalf("animating = true after the animation completes")
	}
}

// mirrors onCleanup(() => clearInterval(timer)): a tick from a superseded
// animation (an older generation) must not touch state — here the item is
// hidden mid-fade, and a stale interval tick from before that must not
// resurrect the alpha.
func TestFadeAnimAdvanceIgnoresStaleGeneration(t *testing.T) {
	f := newFadeAnim(false)
	f.Sync(true, true)
	staleGen := f.gen
	f.start = time.Now().Add(-fadeAnimDuration / 2)

	// Hidden before the stale tick arrives: bumps the generation and resets
	// alpha to 0.
	f.Sync(false, true)

	cmd := f.Advance(fadeTickMsg{anim: f, gen: staleGen})
	if cmd != nil {
		t.Fatalf("Advance with a stale generation returned a Cmd, want nil")
	}
	if f.Alpha() != 0 {
		t.Fatalf("Alpha() = %v after a stale tick, want unchanged 0", f.Alpha())
	}
}

func TestFadeAnimAdvanceIgnoresWrongTarget(t *testing.T) {
	a := newFadeAnim(false)
	b := newFadeAnim(false)
	a.Sync(true, true)
	a.start = time.Now().Add(-fadeAnimDuration / 2)

	if cmd := b.Advance(fadeTickMsg{anim: a, gen: a.gen}); cmd != nil {
		t.Fatalf("b.Advance(a's tick) returned a Cmd, want nil")
	}
	if b.Alpha() != 0 {
		t.Fatalf("b.Alpha() = %v after another anim's tick, want unchanged 0", b.Alpha())
	}
}

// --- debouncer: ports createDebouncedSignal from util/signal.ts -----------

func TestDebouncerSetThenApplyCommitsValue(t *testing.T) {
	d := newDebouncer("")
	cmd := d.Set("ab", time.Millisecond)
	msg, ok := cmd().(debouncedMsg)
	if !ok {
		t.Fatalf("Set's Cmd produced %T, want debouncedMsg", cmd())
	}
	if !d.Apply(msg) {
		t.Fatalf("Apply(msg) = false, want true for the current generation")
	}
	if d.Value() != "ab" {
		t.Fatalf("Value() = %q, want %q", d.Value(), "ab")
	}
}

// mirrors: `if (timer) clearTimeout(timer)` — a second Set before the first
// fires supersedes it; the stale timer's eventual fire must not apply.
func TestDebouncerSupersededSetIsIgnored(t *testing.T) {
	d := newDebouncer("x")
	staleCmd := d.Set("a", time.Millisecond)
	freshCmd := d.Set("b", time.Millisecond)

	staleMsg := staleCmd().(debouncedMsg)
	if d.Apply(staleMsg) {
		t.Fatalf("Apply(stale) = true, want false (superseded by a later Set)")
	}
	if d.Value() != "x" {
		t.Fatalf("Value() = %q after a stale Apply, want unchanged %q", d.Value(), "x")
	}

	freshMsg := freshCmd().(debouncedMsg)
	if !d.Apply(freshMsg) {
		t.Fatalf("Apply(fresh) = false, want true")
	}
	if d.Value() != "b" {
		t.Fatalf("Value() = %q, want %q", d.Value(), "b")
	}
}

func TestDebouncerApplyIgnoresOtherInstance(t *testing.T) {
	a := newDebouncer("a0")
	b := newDebouncer("b0")
	msg := b.Set("b1", time.Millisecond)().(debouncedMsg)
	if a.Apply(msg) {
		t.Fatalf("a.Apply(b's msg) = true, want false")
	}
	if a.Value() != "a0" {
		t.Fatalf("a.Value() = %q after another debouncer's msg, want unchanged %q", a.Value(), "a0")
	}
}

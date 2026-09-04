package session

import "github.com/langazov/gocode-go/internal/event"

// Events announcing that a run has parked on the user: the question tool (and
// plan_enter/plan_exit through it) asking something, or the permission engine
// asking for approval.
//
// Live-only, deliberately. Both requests live in an in-memory map that does
// not survive a restart, so a durable record would replay an ask nobody can
// answer any more. The event is a nudge — "refetch what is pending" — and the
// HTTP endpoints remain the source of truth.
//
// Without these the interface only learned about a pending ask on its 10s
// reconciliation tick, which for a turn that is blocked mid-flight reads as
// the session having simply stopped.
var (
	QuestionAsked   = event.Definition{Type: "session.next.question.asked"}
	QuestionSettled = event.Definition{Type: "session.next.question.settled"}
	PermissionAsked = event.Definition{Type: "session.next.permission.asked"}
)

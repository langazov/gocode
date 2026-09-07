package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/question"
)

// Waiting out a network outage.
//
// A turn that dies because the machine lost its uplink is not a failure of the
// conversation: nothing was said, nothing was decided, and the same request
// would succeed a minute later. Rather than settling such a step as an error —
// which leaves the user to notice, re-read what they typed, and send it again
// — the runner holds the turn and re-attempts it until the network comes back.
//
// The re-attempt *is* the reachability check. There is no separate probe: no
// portable "is the network up" call exists in Go, and a generic one lies
// exactly when it matters (a captive portal answers everything; a VPN split
// tunnel reaches half the internet; the provider alone can be unreachable
// while everything else responds). Re-issuing the request tests the one path
// that has to work, and a dial to a host that is not there fails in
// milliseconds, so the retries cost close to nothing.
// Variables rather than constants only so a test can run the loop without
// spending real minutes in it; nothing outside this package changes them.
var (
	// transportRetryBudget bounds one round of unattended waiting. Reaching
	// it does not end the turn — it hands the decision to the user, who can
	// spend another budget waiting (see awaitNetwork).
	transportRetryBudget = 5 * time.Minute
	// The backoff between attempts, doubling from first to max. Jittered so
	// several sessions coming back at once do not retry in lockstep.
	transportRetryFirstDelay = 2 * time.Second
	transportRetryMaxDelay   = 30 * time.Second
)

// transportDownError marks a step the network cut off before it could do
// anything irreversible. It carries the original error — what the step finally
// settles with if the user stops waiting — and the assistant message the
// attempt had opened, if any, which the wrapper either discards before
// retrying or settles the failure onto.
type transportDownError struct {
	cause              error
	assistantMessageID string
}

func (e *transportDownError) Error() string {
	return "session: transport unreachable: " + e.cause.Error()
}
func (e *transportDownError) Unwrap() error { return e.cause }

// isTransportFailure reports whether an error is the network being unusable
// rather than the provider saying no. Only the former is worth waiting out: a
// 401, a 429 or a malformed response will still be there in five minutes.
//
// The test is deliberately structural (net/syscall types) with one textual
// fallback, rather than string matching alone: a provider's own error text is
// attacker-adjacent input, and "connection refused" appearing inside a model's
// reply must not turn a settled failure into an infinite wait.
func isTransportFailure(err error) bool {
	if err == nil {
		return false
	}
	// The link watcher's own verdict, from a stream it cut short (see
	// watchLinkLoss). Checked first: it is the one case where the machine
	// itself has already answered the question.
	if errors.Is(err, errLinkDown) {
		return true
	}
	// A canceled run is the user's doing, never a network fault, and it must
	// not be waited out — check after the above, since a cut stream reports
	// its cancellation too.
	if errors.Is(err, context.Canceled) {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	for _, errno := range []syscall.Errno{
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		syscall.ECONNABORTED,
		syscall.EHOSTUNREACH,
		syscall.EHOSTDOWN,
		syscall.ENETUNREACH,
		syscall.ENETDOWN,
		syscall.ENETRESET,
		syscall.EPIPE,
		syscall.ETIMEDOUT,
	} {
		if errors.Is(err, errno) {
			return true
		}
	}
	// A connection that ended mid-body, and the stall the llm client reports
	// when a stream goes silent (llm.NewIdleReader) — both are the shape a
	// dropped link takes when no RST ever arrives.
	if errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "stream stalled") {
		return true
	}
	// An *net.OpError with no recognizable cause still means the operation
	// itself (dial, read, write) failed, which no provider response can.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	// http.Client surfaces its own deadline as DeadlineExceeded; with no
	// absolute Timeout left on the streaming client (see llm.NewStreamHTTPClient)
	// the remaining sources are the transport's response-header bound and the
	// dial timeout, both of which mean nothing answered.
	return errors.Is(err, context.DeadlineExceeded)
}

// Asker puts a question to the user and blocks until it is answered. It is
// question.Service in the running binary; nil in a headless runner, which
// makes every ask below fall through to its non-interactive default.
type Asker interface {
	Ask(ctx context.Context, input question.AskInput) ([]question.Answer, error)
}

// Labels for the outage question. Compared against the answer, so they are
// constants rather than literals repeated at both ends.
const (
	keepWaitingLabel = "Keep waiting"
	stopWaitingLabel = "Cancel the turn"
)

// networkHold carries one turn's retry budget across its attempts. Its zero
// value is a turn that has not yet hit an outage.
type networkHold struct {
	deadline time.Time
	delay    time.Duration
}

// awaitNetwork holds a turn whose step could not reach the provider, and
// reports whether to re-attempt it.
//
// It waits in rounds. Within a round it backs off and retries unattended until
// transportRetryBudget is spent; then it asks the user whether to keep
// waiting, and an affirmative answer buys another round. The budget lives on
// the caller's hold, not on this call, precisely so a string of failed
// attempts spends one budget between them instead of restarting it each time.
//
// The second return value is what the step should settle with once waiting is
// over: the original transport error when the user gives up, or the context's
// error when the turn was interrupted while it waited.
//
// With no Asker wired (a headless runner, a subagent) there is nobody to ask,
// so the first exhausted budget ends the wait rather than parking forever.
func (r *Runner) awaitNetwork(runCtx, ctx context.Context, sessionID string, down *transportDownError, hold *networkHold) (retry bool, settleWith error) {
	if hold.delay == 0 {
		hold.deadline = time.Now().Add(transportRetryBudget)
		hold.delay = transportRetryFirstDelay
	}
	if time.Now().After(hold.deadline) {
		keep, err := r.askKeepWaiting(runCtx, ctx, sessionID, down)
		if err != nil {
			return false, err
		}
		if !keep {
			return false, down.cause
		}
		hold.deadline = time.Now().Add(transportRetryBudget)
		hold.delay = transportRetryFirstDelay
	}
	r.publishWaiting(ctx, sessionID, down.cause, hold.delay)
	if err := waitBeforeRetry(runCtx, hold.delay); err != nil {
		return false, err
	}
	if hold.delay *= 2; hold.delay > transportRetryMaxDelay {
		hold.delay = transportRetryMaxDelay
	}
	return true, nil
}

// waitBeforeRetry sleeps out the backoff, and cuts it short the instant the
// machine's connection comes back.
//
// The rule is a transition, not a state: the wait ends early when the link is
// observed going from unusable to usable, never merely because it is usable.
// Both halves matter. A laptop that rejoins Wi-Fi two seconds into a
// thirty-second backoff retries at once instead of idling for the other
// twenty-eight — that is the whole point of watching link state. But a link
// that was up all along says nothing new: the fault is somewhere past the
// first hop, and retrying early would only hammer a provider that is down, so
// the timer governs.
//
// Watching for the transition rather than the initial state is what catches
// the common flap: the request fails, the backoff starts while the interface
// is still nominally up, and only then does the interface actually go away and
// come back. Sampling once at the start would have missed that entirely.
//
// Kernel notifications (linkwatch_*.go) make the observation immediate where
// they exist; the poll is the backstop for platforms without them, a sandbox
// that blocks the socket, and messages the kernel dropped.
func waitBeforeRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(jitter(delay))
	defer timer.Stop()
	watch, stopWatching := context.WithCancel(ctx)
	defer stopWatching()
	changes := watchLink(watch)
	poll := time.NewTicker(linkPollIntervalVar)
	defer poll.Stop()

	up := linkIsUp()
	for {
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-changes:
			// Something about the network changed; the interface table says
			// what, exactly as it does for the poll below.
		case <-poll.C:
		}
		if now := linkIsUp(); now != up {
			up = now
			if up {
				return nil
			}
		}
	}
}

// askKeepWaiting puts the outage to the user. Declining the question at all
// (escape, or a client that rejects it) reads as "stop waiting": the user
// dismissed a prompt about a turn that is going nowhere.
func (r *Runner) askKeepWaiting(runCtx, ctx context.Context, sessionID string, down *transportDownError) (bool, error) {
	if r.Asker == nil {
		return false, nil
	}
	r.publishWaiting(ctx, sessionID, down.cause, 0)
	answers, err := r.Asker.Ask(runCtx, question.AskInput{
		SessionID: sessionID,
		Questions: []question.Prompt{{
			Header: "Network",
			Question: fmt.Sprintf(
				"Still cannot reach the provider after %s (%s). Keep waiting?",
				transportRetryBudget, down.cause,
			),
			Options: []question.Option{
				{Label: keepWaitingLabel, Description: fmt.Sprintf("Retry for another %s", transportRetryBudget)},
				{Label: stopWaitingLabel, Description: "Stop and report the connection error"},
			},
		}},
	})
	if err != nil {
		// An interrupt cancels the ask too; that is the run ending, not a
		// choice about the network.
		if runCtx.Err() != nil {
			return false, runCtx.Err()
		}
		return false, nil
	}
	for _, answer := range answers {
		for _, label := range answer {
			if label == keepWaitingLabel {
				return true, nil
			}
		}
	}
	return false, nil
}

// publishWaiting announces the hold so the interface can say what is going on
// instead of showing a turn that has silently stopped moving. Live-only: a
// past outage is not worth replaying into a reopened session. retryIn of 0
// means the runner is not counting down but waiting on the user's answer.
func (r *Runner) publishWaiting(ctx context.Context, sessionID string, cause error, retryIn time.Duration) {
	if r.Bus == nil {
		return
	}
	// Best effort by construction: this is a progress notice, and failing the
	// turn because the notice could not be delivered would be absurd.
	_, _ = r.Bus.Publish(ctx, StepWaiting, map[string]any{
		"sessionID": sessionID,
		"timestamp": nowMillis(),
		"error":     cause.Error(),
		"retryInMS": retryIn.Milliseconds(),
		// Whether this machine has a usable interface at all. "The link is
		// down" is worth saying plainly — it is the one case the user can
		// actually fix — and it reads very differently from a provider that
		// cannot be reached over a link that is perfectly fine.
		"linkDown": !linkIsUp(),
	}, event.PublishOptions{})
}

// jitter spreads retries by up to a quarter of the delay so sessions that lost
// the network together do not all come back at the same instant.
func jitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	return delay + time.Duration(rand.Int63n(int64(delay/4)+1))
}

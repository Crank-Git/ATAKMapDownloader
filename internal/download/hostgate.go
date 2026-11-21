package download

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// maxHostRequests is the number of requests that one host gets at the same time (FR-polite-downloading-8).
const maxHostRequests = 4

// hostGate holds the request rate, the open requests and the pause of one host pattern.
type hostGate struct {
	// limiter has a burst of 1, so a host at 8 per second gets at most 9 requests in any 1 s window.
	limiter *rate.Limiter
	slots   chan struct{}

	mu          sync.Mutex
	pausedUntil time.Time
}

func newHostGate(limit rate.Limit) *hostGate {
	return &hostGate{
		limiter: rate.NewLimiter(limit, 1),
		slots:   make(chan struct{}, maxHostRequests),
	}
}

// acquire returns when the gate lets one request go to the host.
// The caller calls release after the response.
// It returns the context error when ctx ends first.
func (g *hostGate) acquire(ctx context.Context) error {
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		if err := wait(ctx, g.pauseLeft()); err != nil {
			g.release()
			return err
		}
		if err := g.limiter.Wait(ctx); err != nil {
			g.release()
			return err
		}
		// A response to another request can pause the host during the limiter wait.
		if g.pauseLeft() <= 0 {
			return nil
		}
	}
}

func (g *hostGate) release() {
	<-g.slots
}

// pause holds every request to the host for d (FR-polite-downloading-11).
// A shorter pause does not end a longer pause that is in effect.
func (g *hostGate) pause(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until := time.Now().Add(d); until.After(g.pausedUntil) {
		g.pausedUntil = until
	}
}

func (g *hostGate) pauseLeft() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Until(g.pausedUntil)
}

// gateRegistry holds one gate for each host pattern.
type gateRegistry struct {
	mu    sync.Mutex
	gates map[string]*hostGate
}

func newGateRegistry() *gateRegistry {
	return &gateRegistry{gates: map[string]*hostGate{}}
}

// hostGates is shared by every job of the process, so two jobs on one host share one limit.
var hostGates = newGateRegistry()

// gate returns the gate of hostPattern at rps requests per second.
func (r *gateRegistry) gate(hostPattern string, rps float64) *hostGate {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.gates[hostPattern]
	if !ok {
		g = newHostGate(rate.Limit(rps))
		r.gates[hostPattern] = g
		return g
	}
	// The policy table gives one rate per host pattern, so only a test changes the rate of a gate.
	if g.limiter.Limit() != rate.Limit(rps) {
		g.limiter.SetLimit(rate.Limit(rps))
	}
	return g
}

// wait returns after d, or with the context error when ctx ends first (FR-polite-downloading-13).
func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// maxBackoffSteps gives the schedule 1 s, 2 s, 4 s, then 8 s at most (FR-polite-downloading-10).
const maxBackoffSteps = 3

// backoffDelay returns the wait after the given number of consecutive refusals.
// The jitter is a fraction from -0.2 to 0.2 that changes the wait.
func backoffDelay(base time.Duration, refusals int, jitter float64) time.Duration {
	step := min(max(refusals-1, 0), maxBackoffSteps)
	return time.Duration(float64(base<<step) * (1 + jitter))
}

// maxRetryAfterSeconds caps the parsed number of seconds, so the conversion to a duration cannot overflow.
// Any value above 300 s stops the job, so the cap changes no result.
const maxRetryAfterSeconds = 1_000_000

// parseRetryAfter returns the wait that a Retry-After value asks for, as seconds or as an HTTP date.
// A date in the past gives no wait.
// It returns false when the value is neither.
func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if secs, err := strconv.Atoi(value); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(min(secs, maxRetryAfterSeconds)) * time.Second, true
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return max(date.Sub(now), 0), true
}

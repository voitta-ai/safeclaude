package main

import (
	"fmt"
	"sync"
	"time"

	nfs "github.com/willscott/go-nfs"
)

// The Ask flow: a rule set to ActAsk parks the operation instead of denying
// it. The NFS reply is JUKEBOX ("resource temporarily offline, retry") — the
// macOS client silently retries while the calling process stays blocked in
// the syscall, so the agent never sees an error while the human decides.
// Each retry re-checks the pending entry; a resolution (from the dashboard
// or socket) makes the next retry succeed or fail for real. Timeout denies.

// errJukebox is recognized by the vendored go-nfs formatter patch and sent
// to the client as NFS3ERR_JUKEBOX.
func errJukebox() error {
	return &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusJukebox, WrappedErr: nil}
}

type PendingAsk struct {
	ID       string    `json:"id"`
	Op       string    `json:"op"`
	Path     string    `json:"path"`
	Category Category  `json:"category"`
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`

	decision *bool // nil = pending; true = allow; false = deny
	decided  time.Time
}

// Asks is the pending-approval registry, keyed by op+path so every NFS
// retry of the same blocked operation maps to one question.
type Asks struct {
	mu      sync.Mutex
	pending map[string]*PendingAsk
	timeout time.Duration
	seq     int
	// onAsk fires once per new question (not per retry) — control pushes
	// it to subscribers, which raises the dashboard.
	onAsk func(PendingAsk)
}

func NewAsks(timeout time.Duration, onAsk func(PendingAsk)) *Asks {
	return &Asks{
		pending: make(map[string]*PendingAsk),
		timeout: timeout,
		onAsk:   onAsk,
	}
}

// Check is called from the NFS hot path (non-blocking). It returns:
//   - errJukebox while the question is open (client will retry)
//   - nil when the user allowed
//   - os.ErrPermission-shaped deny when the user denied or it timed out
func (a *Asks) Check(op, path string, cat Category) (verdict string, err error) {
	key := op + " " + path
	a.mu.Lock()
	e, ok := a.pending[key]
	now := time.Now()

	if !ok {
		a.seq++
		e = &PendingAsk{
			ID:       fmt.Sprintf("ask-%d", a.seq),
			Op:       op,
			Path:     path,
			Category: cat,
			Created:  now,
			Expires:  now.Add(a.timeout),
		}
		a.pending[key] = e
		cb := a.onAsk
		ask := *e
		a.mu.Unlock()
		if cb != nil {
			cb(ask)
		}
		return "pending", errJukebox()
	}

	// Existing question: resolved?
	if e.decision == nil && now.After(e.Expires) {
		f := false
		e.decision = &f // timeout = deny
		e.decided = now
	}
	switch {
	case e.decision == nil:
		a.mu.Unlock()
		return "pending", errJukebox()
	case *e.decision:
		// keep the grant briefly so multi-RPC operations (chunked reads,
		// lookup+read pairs) all pass, then forget → next access asks again
		if now.Sub(e.decided) > 30*time.Second {
			delete(a.pending, key)
		}
		a.mu.Unlock()
		return "allow", nil
	default:
		if now.Sub(e.decided) > 30*time.Second {
			delete(a.pending, key)
		}
		a.mu.Unlock()
		return "deny", errPermission
	}
}

// Resolve answers a pending question by id. Returns the ask (for rule
// persistence when remember=true) and whether it was found.
func (a *Asks) Resolve(id string, allow bool) (PendingAsk, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.pending {
		if e.ID == id && e.decision == nil {
			e.decision = &allow
			e.decided = time.Now()
			return *e, true
		}
	}
	return PendingAsk{}, false
}

// Pending lists open questions (for app reconnect).
func (a *Asks) Pending() []PendingAsk {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	var out []PendingAsk
	for _, e := range a.pending {
		if e.decision == nil && now.Before(e.Expires) {
			out = append(out, *e)
		}
	}
	return out
}

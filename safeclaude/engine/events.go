package main

import (
	"sync"
	"time"
)

// Event is one audited filesystem operation, as seen through the puppet mount.
type Event struct {
	Seq      uint64    `json:"seq"`
	Time     time.Time `json:"time"`
	Op       string    `json:"op"`       // READ WRITE CREATE DELETE LIST RENAME MKDIR SYMLNK CHMOD CHOWN STAT
	Path     string    `json:"path"`     // mount-relative, "/" separated
	Category Category  `json:"category"` // see classify.go
	Action   string    `json:"action"`   // allow | deny | hide
	Severity string    `json:"severity"` // info | notice | warning | critical
	Notify   bool      `json:"notify"`   // true when the matched rule is block+notify
}

// SessionStats is the aggregate view served to the menu bar app and report.
type SessionStats struct {
	Started     time.Time            `json:"started"`
	Repo        string               `json:"repo"`
	TotalOps    uint64               `json:"total_ops"`
	Denials     uint64               `json:"denials"`
	Hidden      uint64               `json:"hidden"`
	ByCategory  map[Category]uint64  `json:"by_category"`
	DeniedPaths map[string]uint64    `json:"denied_paths"` // path -> deny count
	ByAction    map[string]uint64    `json:"by_action"`
	// category -> action -> count, for per-category outcome breakdowns
	ByCatAction map[Category]map[string]uint64 `json:"by_cat_action"`
}

// EventBus fans events out to the session store and any live subscribers,
// and retains the full event list for report generation.
type EventBus struct {
	mu    sync.Mutex
	seq   uint64
	subs  map[chan Event]struct{}
	all   []Event
	stats SessionStats
}

func NewEventBus(repo string) *EventBus {
	return &EventBus{
		subs: make(map[chan Event]struct{}),
		stats: SessionStats{
			Started:     time.Now(),
			Repo:        repo,
			ByCategory:  make(map[Category]uint64),
			DeniedPaths: make(map[string]uint64),
			ByAction:    make(map[string]uint64),
			ByCatAction: make(map[Category]map[string]uint64),
		},
	}
}

func (b *EventBus) Publish(e Event) Event {
	b.mu.Lock()
	b.seq++
	e.Seq = b.seq
	e.Time = time.Now()
	b.all = append(b.all, e)

	b.stats.TotalOps++
	b.stats.ByCategory[e.Category]++
	b.stats.ByAction[e.Action]++
	ca := b.stats.ByCatAction[e.Category]
	if ca == nil {
		ca = make(map[string]uint64)
		b.stats.ByCatAction[e.Category] = ca
	}
	ca[e.Action]++
	switch e.Action {
	case "deny":
		b.stats.Denials++
		b.stats.DeniedPaths[e.Path]++
	case "hide":
		b.stats.Hidden++
	}

	for ch := range b.subs {
		select { // never block the NFS hot path on a slow subscriber
		case ch <- e:
		default:
		}
	}
	b.mu.Unlock()
	return e
}

func (b *EventBus) Subscribe() (ch chan Event, cancel func()) {
	ch = make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
		close(ch)
	}
}

func (b *EventBus) Stats() SessionStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.stats // shallow copy, then deep-copy maps
	s.ByCategory = make(map[Category]uint64, len(b.stats.ByCategory))
	for k, v := range b.stats.ByCategory {
		s.ByCategory[k] = v
	}
	s.DeniedPaths = make(map[string]uint64, len(b.stats.DeniedPaths))
	for k, v := range b.stats.DeniedPaths {
		s.DeniedPaths[k] = v
	}
	s.ByAction = make(map[string]uint64, len(b.stats.ByAction))
	for k, v := range b.stats.ByAction {
		s.ByAction[k] = v
	}
	s.ByCatAction = make(map[Category]map[string]uint64, len(b.stats.ByCatAction))
	for c, m := range b.stats.ByCatAction {
		cp := make(map[string]uint64, len(m))
		for k, v := range m {
			cp[k] = v
		}
		s.ByCatAction[c] = cp
	}
	return s
}

func (b *EventBus) Events() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, len(b.all))
	copy(out, b.all)
	return out
}

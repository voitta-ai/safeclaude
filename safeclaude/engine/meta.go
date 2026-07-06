package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MetaMode is the three-position switch used by the meta controls.
type MetaMode string

const (
	MetaOff  MetaMode = "off"  // block outright
	MetaSelf MetaMode = "self" // allow content authored by the repo's user; anything else falls to the grid
	MetaAsk  MetaMode = "ask"  // park the operation for a live decision
)

func (m MetaMode) valid() bool { return m == MetaOff || m == MetaSelf || m == MetaAsk }

// MetaControls is the policy layer between explicit path overrides and the
// per-category grid: overrides > meta > grid.
//
//   - AllowClaudeWrites: when on, every write to a claude-* / mcp-config path
//     is permitted regardless of the grid.
//   - LocalSkills / LocalHooks / Memory gate READS of those categories
//     (writes belong to AllowClaudeWrites or the grid).
//   - GitControls gates git hooks + git config on both axes — the claude
//     toggle doesn't cover git, and writes there are the injection shape.
//     In "self" mode a mutation is never auto-allowed; it falls to the grid.
type MetaControls struct {
	AllowClaudeWrites *bool    `json:"allow_claude_writes,omitempty"`
	LocalSkills       MetaMode `json:"local_skills,omitempty"`
	LocalHooks        MetaMode `json:"local_hooks,omitempty"`
	Memory            MetaMode `json:"memory,omitempty"`
	GitControls       MetaMode `json:"git_controls,omitempty"`
}

// DefaultMeta is the factory state: claude writes open, everything
// self-gated.
func DefaultMeta() *MetaControls {
	t := true
	return &MetaControls{
		AllowClaudeWrites: &t,
		LocalSkills:       MetaSelf,
		LocalHooks:        MetaSelf,
		Memory:            MetaSelf,
		GitControls:       MetaSelf,
	}
}

func (m *MetaControls) claudeWritesAllowed() bool {
	return m == nil || m.AllowClaudeWrites == nil || *m.AllowClaudeWrites
}

// mode returns the switch governing this category/axis, or "" when no meta
// switch applies and the grid should decide.
func (m *MetaControls) mode(cat Category, writeOp bool) MetaMode {
	if m == nil {
		return ""
	}
	switch cat {
	case CatClaudeSkill:
		if !writeOp {
			return m.LocalSkills
		}
	case CatClaudeHook:
		if !writeOp {
			return m.LocalHooks
		}
	case CatClaudeMemory:
		if !writeOp {
			return m.Memory
		}
	case CatVCSHooks, CatVCSConfig:
		return m.GitControls
	}
	return ""
}

// fillFrom completes fields a rules file written by an older build (or a
// hand edit) may lack.
func (m *MetaControls) fillFrom(d *MetaControls) {
	if m.AllowClaudeWrites == nil {
		m.AllowClaudeWrites = d.AllowClaudeWrites
	}
	if !m.LocalSkills.valid() {
		m.LocalSkills = d.LocalSkills
	}
	if !m.LocalHooks.valid() {
		m.LocalHooks = d.LocalHooks
	}
	if !m.Memory.valid() {
		m.Memory = d.Memory
	}
	if !m.GitControls.valid() {
		m.GitControls = d.GitControls
	}
}

// SelfCheck answers "was this created or last edited by the repo's user?"
// for MetaSelf. Mine means: untracked (not yet in git), or the last commit
// touching the file is authored by `git config user.email`. Files under
// .git/ are never tracked, so for those "mine" means the file predates this
// session — set up by the human, not dropped by the agent while it ran.
// Results are cached briefly: this runs on the NFS hot path (including
// directory-listing filters).
type SelfCheck struct {
	root    string
	started time.Time

	mu        sync.Mutex
	inited    bool
	isRepo    bool
	email     string
	tracked   map[string]bool
	trackedAt time.Time
	verdicts  map[string]selfVerdict
}

type selfVerdict struct {
	mine bool
	at   time.Time
}

const selfTTL = 10 * time.Second

func NewSelfCheck(root string) *SelfCheck {
	return &SelfCheck{root: root, started: time.Now(), verdicts: map[string]selfVerdict{}}
}

func (s *SelfCheck) git(args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", s.root}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// Mine reports whether the mount-relative path is self-authored. cat routes
// .git paths to the mtime test (git history can't attribute those).
func (s *SelfCheck) Mine(rel string, cat Category) bool {
	if cat == CatVCSHooks || cat == CatVCSConfig {
		st, err := os.Stat(filepath.Join(s.root, filepath.FromSlash(rel)))
		return err == nil && st.ModTime().Before(s.started)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.inited {
		s.inited = true
		if out, err := s.git("rev-parse", "--is-inside-work-tree"); err == nil && out == "true" {
			s.isRepo = true
			s.email, _ = s.git("config", "user.email")
		}
	}
	if !s.isRepo {
		return true // no git at all: nothing is "in git" yet
	}

	now := time.Now()
	if s.tracked == nil || now.Sub(s.trackedAt) >= selfTTL {
		s.tracked = map[string]bool{}
		s.trackedAt = now
		if out, err := s.git("ls-files", "-z"); err == nil {
			for _, f := range strings.Split(out, "\x00") {
				if f != "" {
					s.tracked[f] = true
				}
			}
		}
	}
	if !s.tracked[rel] {
		return true // not yet committed
	}
	if s.email == "" {
		return false // no identity to attribute against
	}
	if v, ok := s.verdicts[rel]; ok && now.Sub(v.at) < selfTTL {
		return v.mine
	}
	author, err := s.git("log", "-1", "--format=%ae", "--", rel)
	mine := err == nil && author != "" && strings.EqualFold(author, s.email)
	s.verdicts[rel] = selfVerdict{mine: mine, at: now}
	return mine
}

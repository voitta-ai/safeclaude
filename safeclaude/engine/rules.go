package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// Action is what the policy does when a rule matches.
type Action string

const (
	ActAllow  Action = "allow"
	ActBlock  Action = "block"  // deny the op (EPERM)
	ActHide   Action = "hide"   // pretend the file does not exist
	ActNotify Action = "notify" // block + push a notification event
	ActAsk    Action = "ask"    // park the op (NFS jukebox) until the user decides
)

func (a Action) blocks() bool { return a == ActBlock || a == ActHide || a == ActNotify }

// PathOverride pins an exact path or glob to an action; overrides beat
// category rules. Retroactive "approve" appends an allow override.
type PathOverride struct {
	Glob   string `json:"glob"`
	Action Action `json:"action"`
}

// RuleSet is the live policy. Read on every NFS op via atomic pointer —
// mutation replaces the whole value, so the hot path never takes a lock.
type RuleSet struct {
	// Read/list rules per category.
	Read map[Category]Action `json:"read"`
	// Write/mutate rules per category (writes to agent-steering files are
	// the dangerous case, so they get their own axis).
	Write     map[Category]Action `json:"write"`
	Overrides []PathOverride      `json:"overrides"`
	// Meta controls sit between path overrides and the category grid.
	Meta *MetaControls `json:"meta,omitempty"`
}

// DenyAllRules is the factory default: everything blocked-with-notification
// on both axes, so a fresh install is both locked down and loud about it.
// The user opens up from here — first in the Default Policy (the template
// new sessions copy), then per session.
func DenyAllRules() *RuleSet {
	r := &RuleSet{
		Read:  map[Category]Action{},
		Write: map[Category]Action{},
	}
	for _, c := range AllCategories {
		r.Read[c] = ActAsk
		r.Write[c] = ActAsk
	}
	// Git metadata is inert (HEAD, refs, index, objects) and git is simply
	// broken without it. Ordinary project files ("source", the catch-all)
	// are what the agent is here to work on — allowed but fully audited.
	// Everything that can steer the agent or execute code stays gated.
	r.Read[CatVCSMetadata] = ActAllow
	r.Write[CatVCSMetadata] = ActAllow
	r.Read[CatSource] = ActAllow
	r.Write[CatSource] = ActAllow
	// Config reads are routine (git consults config on nearly every command)
	// and rarely sensitive; the danger is writes (fsmonitor/hooksPath), which
	// stay gated — as do hook reads and writes.
	r.Read[CatVCSConfig] = ActAllow
	r.Meta = DefaultMeta()
	return r
}

// merge overlays src onto dst (dst keeps entries src lacks, e.g. categories
// added after the file was written).
func (dst *RuleSet) merge(src *RuleSet) {
	for k, v := range src.Read {
		dst.Read[k] = v
	}
	for k, v := range src.Write {
		dst.Write[k] = v
	}
	if src.Overrides != nil {
		dst.Overrides = src.Overrides
	}
	if src.Meta != nil {
		m := *src.Meta
		m.fillFrom(dst.Meta) // dst always carries factory meta
		dst.Meta = &m
	}
}

func isWriteOp(op string) bool {
	switch op {
	case "WRITE", "CREATE", "DELETE", "RENAME", "MKDIR", "SYMLNK", "CHMOD", "CHOWN":
		return true
	}
	return false
}

// Decide returns the action for an operation on a path. Precedence:
// explicit path overrides, then meta controls, then the category grid.
// mine is consulted lazily, only when a MetaSelf switch applies.
func (r *RuleSet) Decide(op, path string, cat Category, mine func(string, Category) bool) Action {
	clean := strings.Trim(filepath.ToSlash(filepath.Clean("/"+path)), "/")
	for _, o := range r.Overrides {
		if ok, _ := filepath.Match(o.Glob, clean); ok || o.Glob == clean {
			return o.Action
		}
	}
	writeOp := isWriteOp(op)
	// Enumerating a .claude directory itself is the gateway Claude Code's
	// startup scan and skill discovery walk through; it reveals only entry
	// names. Depending on the client, that arrives as LIST (READDIR) or as a
	// plain READ open of the directory — exempt both. The guard belongs on
	// the files inside, which each get their own decision.
	if (op == "LIST" || op == "READ") && (clean == ".claude" || strings.HasSuffix(clean, "/.claude")) {
		return ActAllow
	}
	if writeOp && cat.IsClaudeArtifact() && r.Meta.claudeWritesAllowed() {
		return ActAllow
	}
	switch r.Meta.mode(cat, writeOp) {
	case MetaOff:
		return ActBlock
	case MetaAsk:
		return ActAsk
	case MetaSelf:
		if !writeOp && mine != nil && mine(clean, cat) {
			return ActAllow
		}
		// not provably self-authored (or a mutation): the grid decides
	}
	table := r.Read
	if writeOp {
		table = r.Write
	}
	if a, ok := table[cat]; ok {
		return a
	}
	return ActAllow
}

func (r *RuleSet) clone() *RuleSet {
	n := &RuleSet{
		Read:      make(map[Category]Action, len(r.Read)),
		Write:     make(map[Category]Action, len(r.Write)),
		Overrides: append([]PathOverride(nil), r.Overrides...),
	}
	if r.Meta != nil {
		m := *r.Meta
		if r.Meta.AllowClaudeWrites != nil {
			b := *r.Meta.AllowClaudeWrites
			m.AllowClaudeWrites = &b
		}
		n.Meta = &m
	}
	for k, v := range r.Read {
		n.Read[k] = v
	}
	for k, v := range r.Write {
		n.Write[k] = v
	}
	return n
}

// Rules holds the atomically-swapped live RuleSet plus persistence.
type Rules struct {
	cur  atomic.Pointer[RuleSet]
	file string
}

func readRuleFile(path string) *RuleSet {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var onDisk RuleSet
	if json.Unmarshal(data, &onDisk) != nil || onDisk.Read == nil {
		return nil
	}
	return &onDisk
}

// LoadRules resolves the session's rule set: its own file if it exists
// (session resumed), otherwise a copy of the defaults file (inheritance),
// otherwise deny-all. First run also materializes the defaults file so the
// UI has something to edit before any policy change is made.
func LoadRules(file, defaultsFile string) *Rules {
	rs := &Rules{file: file}
	set := DenyAllRules()
	if own := readRuleFile(file); own != nil {
		set.merge(own)
	} else {
		if def := readRuleFile(defaultsFile); def != nil {
			set.merge(def)
		} else if defaultsFile != "" {
			(&Rules{file: defaultsFile}).persist(set)
		}
		rs.persist(set) // seed this session's copy
	}
	rs.cur.Store(set)
	return rs
}

func (r *Rules) Current() *RuleSet { return r.cur.Load() }

func (r *Rules) persist(set *RuleSet) {
	if r.file == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(r.file), 0o755)
	if data, err := json.MarshalIndent(set, "", "  "); err == nil {
		tmp := r.file + ".tmp"
		if os.WriteFile(tmp, data, 0o644) == nil {
			_ = os.Rename(tmp, r.file)
		}
	}
}

// SetCategory updates one category rule on the given axis ("read"/"write").
func (r *Rules) SetCategory(axis string, cat Category, act Action) {
	set := r.Current().clone()
	if axis == "write" {
		set.Write[cat] = act
	} else {
		set.Read[cat] = act
	}
	r.cur.Store(set)
	r.persist(set)
}

// Approve whitelists an exact path (retroactive allow after a denial).
func (r *Rules) Approve(path string) {
	r.override(path, ActAllow)
}

// DenyPath pins an exact path to plain block: still denied, but no longer
// notify-flavored, so it stops re-alerting on every retry.
func (r *Rules) DenyPath(path string) {
	r.override(path, ActBlock)
}

// SetMeta updates one meta control by wire name.
func (r *Rules) SetMeta(name, value string) error {
	set := r.Current().clone()
	if set.Meta == nil {
		set.Meta = DefaultMeta()
	}
	mode := MetaMode(value)
	switch name {
	case "allow_claude_writes":
		v := value == "on" || value == "true" || value == "yes"
		set.Meta.AllowClaudeWrites = &v
	case "local_skills", "local_hooks", "memory", "git_controls":
		if !mode.valid() {
			return fmt.Errorf("bad meta value %q (want off|self|ask)", value)
		}
		switch name {
		case "local_skills":
			set.Meta.LocalSkills = mode
		case "local_hooks":
			set.Meta.LocalHooks = mode
		case "memory":
			set.Meta.Memory = mode
		case "git_controls":
			set.Meta.GitControls = mode
		}
	default:
		return fmt.Errorf("unknown meta control %q", name)
	}
	r.cur.Store(set)
	r.persist(set)
	return nil
}

func (r *Rules) override(path string, act Action) {
	clean := strings.Trim(filepath.ToSlash(filepath.Clean("/"+path)), "/")
	set := r.Current().clone()
	set.Overrides = append(set.Overrides, PathOverride{Glob: clean, Action: act})
	r.cur.Store(set)
	r.persist(set)
}

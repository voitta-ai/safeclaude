package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
)

// PolicyFS wraps a billy.Filesystem. Every operation is classified,
// checked against the live RuleSet, published to the EventBus, and then
// allowed, denied (EPERM) or hidden (ENOENT).
type PolicyFS struct {
	inner    billy.Filesystem
	realRoot string
	relBase  string // mount-relative prefix of this (possibly chrooted) fs
	rules    *Rules
	bus      *EventBus
	audit    *log.Logger
	asks     *Asks
	self     *SelfCheck
}

func NewPolicyFS(inner billy.Filesystem, realRoot string, rules *Rules, bus *EventBus, audit *log.Logger, asks *Asks, self *SelfCheck) *PolicyFS {
	return &PolicyFS{inner: inner, realRoot: realRoot, rules: rules, bus: bus, audit: audit, asks: asks, self: self}
}

// mine adapts SelfCheck for RuleSet.Decide (MetaSelf switches).
func (p *PolicyFS) mine(path string, cat Category) bool {
	return p.self != nil && p.self.Mine(path, cat)
}

var errPermission = os.ErrPermission

func (p *PolicyFS) rel(path string) string {
	clean := strings.Trim(filepath.ToSlash(filepath.Clean("/"+path)), "/")
	switch {
	case p.relBase == "" && clean == "":
		return "." // repo root, so audit lines aren't blank
	case p.relBase == "":
		return clean
	case clean == "":
		return p.relBase
	}
	return p.relBase + "/" + clean
}

// decisionPath maps macOS AppleDouble sidecars (dir/._name — the resource
// fork written automatically alongside every file over NFS) and .DS_Store to
// their parent file, so one logical write yields one policy decision and one
// approval prompt instead of a confusing second one for invisible metadata.
func decisionPath(rel string) string {
	dir, base := filepath.Split(rel)
	switch {
	case strings.HasPrefix(base, "._"):
		return dir + base[2:]
	case base == ".DS_Store":
		if dir == "" {
			return "."
		}
		return strings.TrimSuffix(dir, "/")
	}
	return rel
}

// decide classifies, rules, publishes and logs one operation. Returns the
// error to surface (nil = allowed).
func (p *PolicyFS) decide(op, path string) error {
	rel := p.rel(path)
	// Group the decision under the parent file for sidecar/metadata paths,
	// but keep `rel` for the audit so nothing is hidden from the record.
	dpath := decisionPath(rel)
	cat := Classify(dpath)
	act := p.rules.Current().Decide(op, dpath, cat, p.mine)

	action := "allow"
	var err error
	switch {
	case act == ActHide:
		action, err = "hide", os.ErrNotExist
	case act == ActAsk:
		var verdict string
		verdict, err = p.asks.Check(op, dpath, cat)
		switch verdict {
		case "pending":
			// question already pushed to the UI by Asks; the client is
			// retrying via jukebox — don't log/publish every retry
			return err
		case "allow":
			action = "allow"
		default:
			action = "deny"
		}
	case act.blocks():
		action, err = "deny", os.ErrPermission
	}

	e := Event{
		Op: op, Path: rel, Category: cat, Action: action,
		Severity: SeverityFor(op, cat, action),
		Notify:   act == ActNotify,
	}
	p.bus.Publish(e)
	if p.audit != nil && !(op == "STAT" && action == "allow") { // stats are too chatty to log
		p.audit.Printf("%-5s %-6s %-14s %s", strings.ToUpper(action), op, cat, rel)
	}
	return err
}

// hidden is a cheap rule-only check used for listing filters and stat,
// so directory walks don't flood the event stream.
func (p *PolicyFS) hidden(path string) bool {
	dpath := decisionPath(p.rel(path))
	return p.rules.Current().Decide("READ", dpath, Classify(dpath), p.mine) == ActHide
}

func (p *PolicyFS) Open(filename string) (billy.File, error) {
	if err := p.decide("READ", filename); err != nil {
		return nil, err
	}
	return p.inner.Open(filename)
}

func (p *PolicyFS) OpenFile(filename string, flag int, perm os.FileMode) (billy.File, error) {
	op := "READ"
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		op = "WRITE"
	}
	if err := p.decide(op, filename); err != nil {
		return nil, err
	}
	return p.inner.OpenFile(filename, flag, perm)
}

func (p *PolicyFS) Create(filename string) (billy.File, error) {
	if err := p.decide("CREATE", filename); err != nil {
		return nil, err
	}
	return p.inner.Create(filename)
}

// probe records a lookup of a hidden path: the NFS client fails at LOOKUP
// (stat) before it ever reaches Open, and a probe for a secret is exactly
// what the audit should capture.
func (p *PolicyFS) probe(path string) error {
	rel := p.rel(path)
	cat := Classify(rel)
	p.bus.Publish(Event{
		Op: "PROBE", Path: rel, Category: cat, Action: "hide",
		Severity: SeverityFor("READ", cat, "hide"),
	})
	if p.audit != nil {
		p.audit.Printf("%-5s %-6s %-14s %s", "HIDE", "PROBE", cat, rel)
	}
	return os.ErrNotExist
}

func (p *PolicyFS) Stat(filename string) (os.FileInfo, error) {
	if p.hidden(filename) {
		return nil, p.probe(filename)
	}
	return p.inner.Stat(filename)
}

func (p *PolicyFS) Lstat(filename string) (os.FileInfo, error) {
	if p.hidden(filename) {
		return nil, p.probe(filename)
	}
	return p.inner.Lstat(filename)
}

func (p *PolicyFS) ReadDir(path string) ([]os.FileInfo, error) {
	if err := p.decide("LIST", path); err != nil {
		return nil, err
	}
	entries, err := p.inner.ReadDir(path)
	if err != nil {
		return nil, err
	}
	kept := entries[:0]
	for _, e := range entries {
		if p.hidden(filepath.Join(path, e.Name())) {
			continue
		}
		kept = append(kept, e)
	}
	return kept, nil
}

func (p *PolicyFS) Rename(oldpath, newpath string) error {
	if err := p.decide("RENAME", oldpath); err != nil {
		return err
	}
	if err := p.decide("RENAME", newpath); err != nil {
		return err
	}
	return p.inner.Rename(oldpath, newpath)
}

func (p *PolicyFS) Remove(filename string) error {
	if err := p.decide("DELETE", filename); err != nil {
		return err
	}
	return p.inner.Remove(filename)
}

func (p *PolicyFS) MkdirAll(filename string, perm os.FileMode) error {
	if err := p.decide("MKDIR", filename); err != nil {
		return err
	}
	return p.inner.MkdirAll(filename, perm)
}

func (p *PolicyFS) Symlink(target, link string) error {
	if err := p.decide("SYMLNK", link); err != nil {
		return err
	}
	return p.inner.Symlink(target, link)
}

func (p *PolicyFS) Readlink(link string) (string, error) {
	if p.hidden(link) {
		return "", os.ErrNotExist
	}
	return p.inner.Readlink(link)
}

func (p *PolicyFS) TempFile(dir, prefix string) (billy.File, error) {
	if err := p.decide("WRITE", filepath.Join(dir, prefix+"*")); err != nil {
		return nil, err
	}
	return p.inner.TempFile(dir, prefix)
}

func (p *PolicyFS) Join(elem ...string) string { return p.inner.Join(elem...) }
func (p *PolicyFS) Root() string               { return p.inner.Root() }

func (p *PolicyFS) Chroot(path string) (billy.Filesystem, error) {
	sub, err := p.inner.Chroot(path)
	if err != nil {
		return nil, err
	}
	child := NewPolicyFS(sub, filepath.Join(p.realRoot, path), p.rules, p.bus, p.audit, p.asks, p.self)
	child.relBase = p.rel(path)
	return child, nil
}

func (p *PolicyFS) realPath(name string) string {
	return filepath.Join(p.realRoot, filepath.Clean("/"+name))
}

func (p *PolicyFS) Chmod(name string, mode os.FileMode) error {
	if err := p.decide("CHMOD", name); err != nil {
		return err
	}
	return os.Chmod(p.realPath(name), mode)
}

func (p *PolicyFS) Chown(name string, uid, gid int) error {
	if err := p.decide("CHOWN", name); err != nil {
		return err
	}
	return os.Chown(p.realPath(name), uid, gid)
}

func (p *PolicyFS) Lchown(name string, uid, gid int) error {
	if err := p.decide("CHOWN", name); err != nil {
		return err
	}
	return os.Lchown(p.realPath(name), uid, gid)
}

func (p *PolicyFS) Chtimes(name string, atime, mtime time.Time) error {
	if p.hidden(name) {
		return os.ErrPermission
	}
	return os.Chtimes(p.realPath(name), atime, mtime)
}

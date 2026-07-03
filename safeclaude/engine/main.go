// safeclaude-engine: userspace NFS server serving a policy-enforced view of
// a real directory, with a live-updatable rule set, a classified event
// stream over a unix control socket, and a session report renderer.
//
// No kernel extensions, no Apple entitlements, no root required on macOS.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"
)

func main() {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".safeclaude")

	root := flag.String("root", "", "real directory to serve a policed view of")
	name := flag.String("name", "", "session name (defaults to basename of root)")
	listen := flag.String("listen", "127.0.0.1:0", "address for the NFS server")
	socket := flag.String("socket", filepath.Join(base, "control.sock"), "unix control socket path")
	rulesFile := flag.String("rules", filepath.Join(base, "rules.json"), "this session's rules file")
	defaultsFile := flag.String("defaults", filepath.Join(base, "default-rules.json"), "default policy template new sessions inherit")
	logPath := flag.String("log", "", "audit log file (in addition to stderr)")
	reportDir := flag.String("reports", filepath.Join(base, "reports"), "directory for session reports")
	// Note: while an ask is pending the macOS NFS client pauses the whole
	// mount, so this timeout bounds how long the session can freeze.
	askTimeout := flag.Duration("ask-timeout", 60*time.Second, "how long an Ask-mode operation waits for a decision before denying")
	flag.Parse()

	if *root == "" {
		log.Fatal("usage: safeclaude-engine -root /path/to/repo")
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		log.Fatal(err)
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		log.Fatalf("root %q is not a readable directory", abs)
	}
	_ = os.MkdirAll(base, 0o755)

	sinks := []io.Writer{os.Stderr}
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("cannot open log file: %v", err)
		}
		sinks = append(sinks, f)
	}
	audit := log.New(io.MultiWriter(sinks...), "", log.Ltime|log.Lmicroseconds)

	if *name == "" {
		*name = filepath.Base(abs)
	}
	bus := NewEventBus(abs)
	rules := LoadRules(*rulesFile, *defaultsFile)

	ctl := &Control{
		bus:   bus,
		rules: rules,
		reportFn: func() (string, error) {
			return RenderReport(bus, rules, *reportDir, *name)
		},
	}
	asks := NewAsks(*askTimeout, func(a PendingAsk) {
		bus.Publish(Event{
			Op: a.Op, Path: a.Path, Category: a.Category, Action: "ask",
			Severity: SeverityFor(a.Op, a.Category, "deny"),
		})
		ctl.PushAsk(a)
	})
	ctl.asks = asks
	if err := ctl.Serve(*socket); err != nil {
		log.Fatalf("control socket: %v", err)
	}

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	// The wrapper script parses this line to learn the port.
	fmt.Printf("LISTENING %d\n", listener.Addr().(*net.TCPAddr).Port)
	audit.Printf("SERVE root=%s socket=%s", abs, *socket)

	pfs := NewPolicyFS(osfs.New(abs), abs, rules, bus, audit, asks)
	handler := nfshelper.NewNullAuthHandler(pfs)
	cached := nfshelper.NewCachingHandler(handler, 1024)
	log.Fatal(nfs.Serve(listener, cached))
}

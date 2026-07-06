package main

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// Control is the unix-socket JSON-lines API the menu bar app (or nc -U)
// talks to. Client sends {"cmd": ...}; server replies and, after
// "subscribe", pushes {"type":"event",...} lines.
type Control struct {
	bus   *EventBus
	rules *Rules
	asks  *Asks
	// reportFn renders the session report and returns its path.
	reportFn func() (string, error)

	mu   sync.Mutex
	subs map[*json.Encoder]func(ctrlReply) bool // live subscribers, for ask pushes
}

type ctrlRequest struct {
	Cmd      string   `json:"cmd"` // subscribe | stats | get_rules | set_rule | set_meta | approve | deny | resolve | pending | report | shutdown | ping
	Axis     string   `json:"axis,omitempty"`
	Category Category `json:"category,omitempty"`
	Action   Action   `json:"action,omitempty"`
	Path     string   `json:"path,omitempty"`
	Name     string   `json:"name,omitempty"`  // set_meta: control name
	Value    string   `json:"value,omitempty"` // set_meta: on|off / off|self|ask
	ID       string   `json:"id,omitempty"`       // resolve: ask id
	Allow    bool     `json:"allow,omitempty"`    // resolve: verdict
	Remember bool     `json:"remember,omitempty"` // resolve: persist as path override
}

type ctrlReply struct {
	Type   string        `json:"type"` // ok | error | stats | rules | event | report | ask | pending
	Error  string        `json:"error,omitempty"`
	Stats  *SessionStats `json:"stats,omitempty"`
	Rules  *RuleSet      `json:"rules,omitempty"`
	Event  *Event        `json:"event,omitempty"`
	Report string        `json:"report,omitempty"`
	Ask    *PendingAsk   `json:"ask,omitempty"`
	Asks   []PendingAsk  `json:"asks,omitempty"`
}

// PushAsk broadcasts a new pending question to every subscribed client.
func (c *Control) PushAsk(a PendingAsk) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, send := range c.subs {
		ask := a
		send(ctrlReply{Type: "ask", Ask: &ask})
	}
}

func (c *Control) Serve(socketPath string) error {
	_ = os.Remove(socketPath)
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go c.handle(conn)
		}
	}()
	return nil
}

func (c *Control) handle(conn net.Conn) {
	defer conn.Close()
	var wmu sync.Mutex
	enc := json.NewEncoder(conn)
	send := func(r ctrlReply) bool {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return enc.Encode(r) == nil
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	var unsubscribe func()
	defer func() {
		if unsubscribe != nil {
			unsubscribe()
		}
		c.mu.Lock()
		delete(c.subs, enc)
		c.mu.Unlock()
	}()

	for scanner.Scan() {
		var req ctrlRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			send(ctrlReply{Type: "error", Error: "bad json"})
			continue
		}
		switch req.Cmd {
		case "ping":
			send(ctrlReply{Type: "ok"})
		case "stats":
			s := c.bus.Stats()
			send(ctrlReply{Type: "stats", Stats: &s})
		case "get_rules":
			send(ctrlReply{Type: "rules", Rules: c.rules.Current()})
		case "set_rule":
			if req.Category == "" || req.Action == "" {
				send(ctrlReply{Type: "error", Error: "set_rule needs category and action"})
				continue
			}
			c.rules.SetCategory(req.Axis, req.Category, req.Action)
			send(ctrlReply{Type: "rules", Rules: c.rules.Current()})
		case "set_meta":
			if err := c.rules.SetMeta(req.Name, req.Value); err != nil {
				send(ctrlReply{Type: "error", Error: err.Error()})
				continue
			}
			log.Printf("control: meta %s = %s", req.Name, req.Value)
			send(ctrlReply{Type: "rules", Rules: c.rules.Current()})
		case "approve":
			if req.Path == "" {
				send(ctrlReply{Type: "error", Error: "approve needs path"})
				continue
			}
			c.rules.Approve(req.Path)
			log.Printf("control: approved %s", req.Path)
			send(ctrlReply{Type: "rules", Rules: c.rules.Current()})
		case "deny":
			if req.Path == "" {
				send(ctrlReply{Type: "error", Error: "deny needs path"})
				continue
			}
			c.rules.DenyPath(req.Path)
			log.Printf("control: denied %s", req.Path)
			send(ctrlReply{Type: "rules", Rules: c.rules.Current()})
		case "resolve":
			ask, ok := c.asks.Resolve(req.ID, req.Allow)
			if !ok {
				send(ctrlReply{Type: "error", Error: "no such pending ask"})
				continue
			}
			if req.Remember {
				if req.Allow {
					c.rules.Approve(ask.Path)
				} else {
					c.rules.DenyPath(ask.Path)
				}
			}
			log.Printf("control: resolved %s %s allow=%v remember=%v", ask.ID, ask.Path, req.Allow, req.Remember)
			send(ctrlReply{Type: "ok"})
		case "pending":
			send(ctrlReply{Type: "pending", Asks: c.asks.Pending()})
		case "report":
			path, err := c.reportFn()
			if err != nil {
				send(ctrlReply{Type: "error", Error: err.Error()})
			} else {
				send(ctrlReply{Type: "report", Report: path})
			}
		case "shutdown":
			// Session deletion: the app unmounts and removes state after
			// this ack; the brief delay lets the reply flush first.
			log.Printf("control: shutdown requested")
			send(ctrlReply{Type: "ok"})
			go func() {
				time.Sleep(200 * time.Millisecond)
				os.Exit(0)
			}()
		case "subscribe":
			if unsubscribe != nil {
				send(ctrlReply{Type: "ok"})
				continue
			}
			ch, cancel := c.bus.Subscribe()
			unsubscribe = cancel
			c.mu.Lock()
			if c.subs == nil {
				c.subs = make(map[*json.Encoder]func(ctrlReply) bool)
			}
			c.subs[enc] = send
			c.mu.Unlock()
			send(ctrlReply{Type: "ok"})
			go func() {
				for e := range ch {
					ev := e
					if !send(ctrlReply{Type: "event", Event: &ev}) {
						return
					}
				}
			}()
		default:
			send(ctrlReply{Type: "error", Error: "unknown cmd"})
		}
	}
}

# SafeClaude

Run Claude Code (or any command) inside a policy-enforced, fully audited view
of a repo. A userspace NFS "puppet" filesystem hides secrets, blocks writes to
agent-steering files (hooks, skills, settings), records every file operation,
and renders a session report. **No root, no kernel extensions, no Apple
entitlements** — works on a stock Mac.

---

## Requirements

- macOS 14+ (tested on 15.7, Apple Silicon)
- Go 1.22+ (`brew install go`)
- Swift 5.9+ toolchain — Xcode **Command Line Tools are enough** (`xcode-select --install`), no Xcode app needed
- `claude` on your PATH (optional — falls back to your shell)

## Build

```bash
git clone <this repo> && cd safeclaude   # repo clones into ./safeclaude

# 1. engine (Go NFS server + policy + report renderer)
cd safeclaude/engine
go build -o safeclaude-engine .

# 2. menu bar app (SwiftUI, built with SPM, wrapped into a .app bundle)
cd ../app
./make-app.sh          # → app/SafeClaude.app, ad-hoc signed
```

Optionally symlink the launcher onto your PATH:

```bash
ln -s "$(pwd)/../safeclaude" /usr/local/bin/safeclaude
```

## Use

```bash
./safeclaude ~/work/myrepo             # launches claude inside the guarded view
./safeclaude ~/work/myrepo zsh         # or any command, e.g. a plain shell
```

What happens:

1. The engine starts and serves a **policed view** of `~/work/myrepo` over
   localhost NFS.
2. The view is mounted at the **stable path** `~/.safeclaude/mounts/myrepo` —
   a plain user mount, no sudo. Stable means Claude Code's per-directory
   state (history, project config) survives across sessions.
3. The **menu bar app** (shield icon) launches if not already running.
4. Your command runs **inside the mount**. It sees the repo minus hidden
   files; blocked writes fail with "permission denied"; everything is logged.
5. On exit: unmount, session **report path printed**, audit log kept.

### Sessions are 1:1 with repos

Running `safeclaude` again on the **same repo** while a session is live
**joins** it — same mount, same engine, same audit stream; no duplicate
mounts. The session ends (unmount + report) when the **last attached client**
exits. Crashed sessions are detected and reclaimed automatically on the next
run. Only a genuinely different repo with the same folder name gets a
suffixed mount (`myrepo-2`).

Session state lives in `~/.safeclaude/sessions/<name>/` (repo path, engine
pid, control socket, attached-client pids) and disappears with the session.

### The menu bar app

Clicking the shield opens a **dashboard panel** (not a menu — sessions never
pile up as menu items), with three tabs:

| Tab | What's in it |
|---|---|
| **Sessions** | One card per live session: connection dot, repo path, live stats (ops / denied / hidden / agent-file reads), pending denials with live waiting timers and **Approve / Deny** buttons per path, **View report**, and a **trash button** — force-ends the session and erases every trace of it (policy, session state, mount, audit logs, reports; optionally Claude Code's conversation history for the mount), so the next run starts factory-fresh |
| **Session Policy** | Read & write rules for **one session** (picker at the top): Allow / Block / Hide / Block & Notify — effective **immediately**, no remount |
| **Default Policy** | The **template new sessions inherit** at start. Factory state: **Block & Notify everything** — open up what you trust. Editing it never touches running sessions |
| **Reports** | Every session report, rendered **inside the panel** (embedded web view) — newest first, list on the left, report on the right |

"View report" on a session card renders that session's report and jumps to
the Reports tab. **Approve** = the denied command simply works on retry;
**Deny** = stays blocked but stops re-alerting for that exact path.

When a denial happens, the dashboard **raises itself** — it follows you onto
any Space (including another app's fullscreen Space) and floats on top until
you interact, so a waiting agent is never stuck behind your editor.

Icon states: filled shield = session active · shield with badge = denials
happened · slashed shield = no engine running.

> First run: allow notifications for "SafeClaude" in System Settings →
> Notifications if you want blocked-access alerts.

### Policy: two layers

1. **Default policy** — `~/.safeclaude/default-rules.json`, the template.
   Factory state: **Block & Notify on everything** (reads and writes, all
   categories) — locked down and loud about it. Edit it in the Default
   Policy tab to define what future sessions start with.
2. **Session policy** — `~/.safeclaude/sessions/<name>/rules.json`, copied
   from the default when the session starts. Edit it live in the Session
   Policy tab (or over the socket); changes affect that session only and die
   with it.

### Meta controls (third column on both policy tabs)

A layer that sits **between** per-path Approve/Deny overrides and the
category grid: overrides > meta > grid.

- **Allow Claude writes** (toggle, default **on**) — every write to a
  claude-* / MCP-config path is permitted; when off, the write grid decides.
- **Local skills / Local hooks / Memory** (three-position, default **Self**) —
  gate *reads* of those categories. **Block**: nothing readable. **Self**:
  readable if the file is not yet in git, or the last commit touching it is
  authored by the repo's `git config user.email`; anything else falls to the
  grid. **Ask**: every read pauses for approval.
- **Git hooks & config** (three-position, default **Self**) — same switch,
  both axes, for `.git/hooks/**` and `.git/config`. Since git never tracks
  its own internals, "self" here means the file predates the session (set up
  by you, not dropped by the agent); mutations in Self mode always fall to
  the grid.

Self verdicts are cached ~10s, so a policy-relevant `git commit` you make
mid-session is picked up within seconds. Set over the socket with
`{"cmd":"set_meta","name":"local_skills","value":"off|self|ask"}` (the
toggle takes `"on"`/`"off"`).

Actions per category × axis (read/write):

- **Allow** — permitted, still audited.
- **Ask** — the file operation is **paused** (the agent's syscall blocks
  mid-call, so it sees no error) while a decision card appears in the
  dashboard: category badge, icon, a plain-language reason it's gated, the
  full path, and a live countdown. **Deny** / **Allow once** / **Allow
  always** (persist an allow for this path) resolve it; the paused operation
  then completes or fails for real within ~5s. No decision before the timeout
  (default 60s) auto-denies. This is the factory default for every gated
  category — the agent is never misled by a phantom denial it might work
  around.

  macOS writes a hidden AppleDouble sidecar (`._name`) next to every file over
  NFS; SafeClaude folds that sidecar (and `.DS_Store`) into its parent's
  decision, so one logical write is **one** prompt, not two.
- **Block** — deny immediately (EPERM).
- **Hide** — invisible in listings, "no such file"; probes still audited.
- **Block & Notify** — EPERM plus a notification, flagged in the audit.

> **Ask trade-off (macOS NFS):** while an Ask is pending the macOS NFS client
> pauses the *whole mount*, so the session is frozen until you answer or it
> times out. That's the price of true syscall-level pausing over NFS — set a
> category to **Block & Notify** instead if you want non-blocking denials with
> retroactive approval.

Categories: Claude skills / hooks / commands / agents / settings / memory,
MCP config, secrets (`.env*`, `*.pem`, `*.key`, `id_rsa*`, `secrets*`,
`credentials*`, `.aws/`, `.ssh/`, …), **git hooks** (`.git/hooks/**` — git
executes these on commit/checkout), **git config** (`.git/config` — its
`fsmonitor`/`hooksPath`/pager/alias keys make git run arbitrary commands;
`fsmonitor` fires on a mere `git status`), **git metadata** (the rest of
`.git/` — HEAD, refs, index, objects; inert bookkeeping polled constantly),
source (**all other files** — the ordinary substance of the repo).

Enumerating a `.claude` directory **itself** (listing or opening the
directory read-only) is always allowed — it's the gateway Claude Code's
startup scan and skill discovery walk through, and reveals only entry
names; each file inside still gets its own decision.

Factory defaults: source, git metadata, and git-config **reads** are
allowed (an agent that can't read the project or git state is useless, and
config reads are routine); git-config **writes**, git hooks (both axes),
and every agent-steering/secret category are Block & Notify. Writes to git
hooks or config are flagged `critical` — that's the injection shape.

### Where things land

| Path | Contents |
|---|---|
| `~/.safeclaude/rules.json` | live policy (persists across sessions) |
| `~/.safeclaude/mounts/<name>` | stable mount point per repo (only while live) |
| `~/.safeclaude/sessions/<name>/` | session state incl. `control.sock` (only while live) |
| `~/.safeclaude/logs/<name>-*.log` | audit log per session |
| `~/.safeclaude/reports/session-<name>-*.html` | session reports (self-contained, printable, light/dark) |

Reports end with an **Inventory** exhibit: every local skill, Claude hook,
memory file and git hook present in the repo at report time (all `.claude`
folders, nested included), each as a click-to-expand row showing the file's
full content, size, mtime and git origin — files whose last commit isn't by
your `git config user.email` are badged **foreign**. The scan reads the
disk directly, so hidden/blocked files still appear. Each row carries
color-coded **read**/**write** verdict badges — what the live session
policy would decide for that file right now, annotated with why ("self",
"grid", "toggle"); the row's left edge is tinted by the read verdict.

## Scripting the engine (optional)

The menu bar app is optional sugar — everything works headless over each
session's socket:

```bash
SOCK=~/.safeclaude/sessions/myrepo/control.sock
printf '{"cmd":"stats"}\n'    | nc -U "$SOCK"
printf '{"cmd":"subscribe"}\n'| nc -U "$SOCK"   # live event stream
printf '{"cmd":"set_rule","axis":"read","category":"secret","action":"allow"}\n' | nc -U "$SOCK"
printf '{"cmd":"approve","path":"src/generated/api.ts"}\n' | nc -U "$SOCK"
printf '{"cmd":"report"}\n'   | nc -U "$SOCK"   # returns report path
printf '{"cmd":"shutdown"}\n' | nc -U "$SOCK"   # engine exits (session deletion)
```

## Architecture

```
┌──────────────┐   NFS (localhost)   ┌──────────────────┐
│ claude, git, │ ──────────────────▶ │  engine (Go)     │──▶ real repo
│ hooks, bash  │    puppet mount     │  classify + rules│
└──────────────┘                     │  event bus       │
                                     └──┬────────────┬──┘
                              unix socket│            │HTML
                                ┌────────▼──────┐  ┌──▼──────────────┐
                                │ SafeClaude.app│  │ session report  │
                                │ (SwiftUI menu │  │ (McKinsey       │
                                │  bar, live)   │  │  Quarterly look)│
                                └───────────────┘  └─────────────────┘
```

- `safeclaude/engine/classify.go` — path → category (claude-skill, claude-hook,
  claude-memory, mcp-config, secret, …)
- `safeclaude/engine/rules.go` — atomically hot-swappable rule set; "approve" appends an
  exact-path allow override
- `safeclaude/engine/control.go` — unix-socket JSON-lines API (stats, subscribe,
  set_rule, approve, report)
- `safeclaude/engine/report.go` — report renderer; light = Quarterly editorial white,
  dark = "Data View" navy, via `prefers-color-scheme`
- `safeclaude/app/Sources/SafeClaudeBar/` — `MenuBarExtra` UI + socket client
- `safeclaude/safeclaude` — wrapper: engine → mount → app → run → unmount → report

## Troubleshooting

- **"engine failed to start"** — leftovers from a crash normally self-heal on
  the next run; if not: `pkill safeclaude-engine; rm -rf ~/.safeclaude/sessions/<name>`.
- **Stale mount that self-heal missed** — `umount -f ~/.safeclaude/mounts/<name>`.
- **Policy flip seems ignored** — only affects the *wrapper's* mount options
  if you mounted manually; use the provided `safeclaude` script (it mounts
  with `nonegnamecache` + 1s attribute cache so toggles apply instantly).
- **No notifications** — ad-hoc-signed apps need one-time approval in System
  Settings → Notifications.

## License

Dual-licensed: **AGPL-3.0-or-later** ([LICENSE](./LICENSE)) or a commercial
license — see [LICENSING.md](./LICENSING.md). Contact: support@voitta.ai.

## Security limits (by design, for now)

- Only the **mounted view** is guarded. Absolute paths (`~/.ssh`, `/etc`)
  bypass it — pair with an outer static sandbox (macOS Seatbelt profile) for
  a full fence.
- NFS cannot attribute an operation to a specific process (Claude vs git vs a
  hook). Per-process attribution needs Apple's Endpoint Security framework.

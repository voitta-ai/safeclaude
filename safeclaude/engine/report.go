package main

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// RenderReport writes a self-contained McKinsey Quarterly–styled HTML session
// report and returns its path. Light mode follows the magazine's editorial
// white pages; dark mode follows its navy "Data View" exhibit pages.
func RenderReport(bus *EventBus, rules *Rules, dir, session string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stats := bus.Stats()
	events := bus.Events()

	type catRow struct {
		Label     string
		Allowed   uint64
		Denied    uint64
		Hidden    uint64
		Total     uint64
		DeniedPct float64 // share of this category's ops that were denied
		Artifact  bool
	}
	var cats []catRow
	var totAllowed, totDenied, totHidden uint64
	for _, c := range AllCategories {
		n := stats.ByCategory[c]
		if n == 0 {
			continue
		}
		ca := stats.ByCatAction[c]
		row := catRow{
			Label:    c.Label(),
			Allowed:  ca["allow"],
			Denied:   ca["deny"],
			Hidden:   ca["hide"],
			Total:    n,
			Artifact: c.IsClaudeArtifact(),
		}
		row.DeniedPct = float64(row.Denied+row.Hidden) / float64(n) * 100
		totAllowed += row.Allowed
		totDenied += row.Denied
		totHidden += row.Hidden
		cats = append(cats, row)
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].Total > cats[j].Total })

	type evRow struct {
		Time     string
		Op       string
		Path     string
		Action   string
		Severity string
	}
	var artifactEvents, sensitive []evRow
	seenArtifact := map[string]bool{}
	var artifactReads, criticalWrites int
	for _, e := range events {
		if e.Category.IsClaudeArtifact() {
			if e.Op == "READ" || e.Op == "LIST" {
				artifactReads++
			}
			if e.Severity == "critical" {
				criticalWrites++
			}
			key := e.Op + " " + e.Path + " " + e.Action
			if !seenArtifact[key] && len(artifactEvents) < 60 {
				seenArtifact[key] = true
				artifactEvents = append(artifactEvents, evRow{
					Time: e.Time.Format("15:04:05"), Op: e.Op, Path: e.Path,
					Action: e.Action, Severity: e.Severity,
				})
			}
		}
		if e.Severity != "info" && len(sensitive) < 100 {
			sensitive = append(sensitive, evRow{
				Time: e.Time.Format("15:04:05"), Op: e.Op, Path: e.Path,
				Action: e.Action, Severity: e.Severity,
			})
		}
	}

	type denyRow struct {
		Path     string
		Count    uint64
		Approved bool
	}
	approved := map[string]bool{}
	for _, o := range rules.Current().Overrides {
		if o.Action == ActAllow {
			approved[o.Glob] = true
		}
	}
	var denies []denyRow
	for p, n := range stats.DeniedPaths {
		denies = append(denies, denyRow{Path: p, Count: n, Approved: approved[p]})
	}
	sort.Slice(denies, func(i, j int) bool { return denies[i].Count > denies[j].Count })

	headline, standfirst := headlineFor(stats, artifactReads, criticalWrites)

	data := map[string]any{
		"Session":        session,
		"Repo":           filepath.Base(stats.Repo),
		"RepoFull":       stats.Repo,
		"Date":           time.Now().Format("January 2, 2006"),
		"Started":        stats.Started.Format("15:04"),
		"Duration":       time.Since(stats.Started).Round(time.Second).String(),
		"Quarter":        fmt.Sprintf("Q%d", (int(time.Now().Month())-1)/3+1),
		"Year":           time.Now().Year(),
		"Headline":       headline,
		"Standfirst":     standfirst,
		"TotalOps":       stats.TotalOps,
		"Denials":        stats.Denials,
		"Hidden":         stats.Hidden,
		"ArtifactReads":  artifactReads,
		"CriticalWrites": criticalWrites,
		"Cats":           cats,
		"TotAllowed":     totAllowed,
		"TotDenied":      totDenied,
		"TotHidden":      totHidden,
		"ArtifactEvents": artifactEvents,
		"Sensitive":      sensitive,
		"Denies":         denies,
		"Generated":      time.Now().Format("Jan 2, 2006 15:04:05"),
	}

	name := fmt.Sprintf("session-%s-%s.html", session, stats.Started.Format("20060102-150405"))
	out := filepath.Join(dir, name)
	f, err := os.Create(out)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := reportTmpl.Execute(f, data); err != nil {
		return "", err
	}
	return out, nil
}

// headlineFor writes the cover line the way the Quarterly would: the single
// most important fact of the session, stated plainly.
func headlineFor(s SessionStats, artifactReads, criticalWrites int) (string, string) {
	switch {
	case criticalWrites > 0:
		return "An Attempt To Rewrite The Rules",
			fmt.Sprintf("The agent made %d write attempt(s) to its own steering files — skills, hooks, or settings. All were intercepted and are itemized below.", criticalWrites)
	case s.Denials > 0:
		return "Stopped At The Gate",
			fmt.Sprintf("%d operations were denied by policy this session, out of %d total. Every access — permitted or not — is on the record.", s.Denials, s.TotalOps)
	case artifactReads > 0:
		return "The Agent Read Its Own Instructions",
			fmt.Sprintf("%d reads touched agent-steering content — skills, hooks, memory, configuration. Nothing was blocked; everything was watched.", artifactReads)
	default:
		return "A Quiet Session",
			fmt.Sprintf("%d file operations, no denials, no surprises. The full audit follows.", s.TotalOps)
	}
}

var reportTmpl = template.Must(template.New("report").Parse(reportHTML))

const reportHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>SafeClaude Session Report — {{.Repo}}</title>
<style>
/* ------------------------------------------------------------------
   McKinsey Quarterly–derived system.
   Light = editorial white pages; dark = navy "Data View" pages.
   Tokens sampled from McKinsey Quarterly Q2 2026.
------------------------------------------------------------------ */
:root {
  --serif: "New York", Georgia, "Times New Roman", serif;
  --sans: -apple-system, "Helvetica Neue", Helvetica, Arial, sans-serif;
  --bg: #ffffff;
  --ink: #111111;
  --muted: #555555;
  --hairline: #c9c9c9;
  --bar-block: #000000;        /* the full-width black bar */
  --accent: #00A9F4;           /* print cyan — big numbers, badges */
  --electric: #2251FF;         /* electric blue */
  --chart-main: #2251FF;
  --chart-artifact: #00A9F4;
  --chart-secret: #2E7E71;     /* teal */
  --chart-track: #efefec;
  --panel: #f4f4f1;
  --warn: #E8A33D;
  --crit: #C6373C;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #051C34;             /* Data View navy */
    --ink: #f4f7fb;
    --muted: #9fb2c8;
    --hairline: rgba(255,255,255,.35);
    --bar-block: #ffffff;
    --accent: #3FBAF3;
    --electric: #A9D6F5;       /* light blue reads better on navy */
    --chart-main: #A9D6F5;
    --chart-artifact: #3FBAF3;
    --chart-secret: #4FA79A;
    --chart-track: rgba(255,255,255,.08);
    --panel: rgba(255,255,255,.05);
    --warn: #F5C066;
    --crit: #F08A8E;
  }
}
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
  background: var(--bg); color: var(--ink);
  font-family: var(--sans); font-size: 15px; line-height: 1.55;
  -webkit-font-smoothing: antialiased;
}
.page { max-width: 760px; margin: 0 auto; padding: 40px 28px 80px; }

/* running head, like the magazine folio line */
.folio {
  display: flex; justify-content: space-between; align-items: baseline;
  border-top: 1.5px solid var(--ink); padding-top: 8px;
  font-size: 10.5px; font-weight: 700; letter-spacing: .18em;
  text-transform: uppercase;
}
.folio .r { font-weight: 400; letter-spacing: .14em; color: var(--muted); }

/* masthead */
.masthead { margin: 48px 0 0; }
.masthead h1 {
  font-family: var(--serif); font-weight: 500;
  font-size: clamp(44px, 9vw, 72px); line-height: .95; letter-spacing: -.01em;
}
.masthead h1 .l2 { display: block; padding-left: 1.4em; }
.estd {
  display: flex; justify-content: space-between;
  border-top: 1px solid var(--ink); border-bottom: 1px solid var(--ink);
  margin-top: 22px; padding: 7px 2px;
  font-size: 10.5px; font-weight: 700; letter-spacing: .2em; text-transform: uppercase;
}
.blackbar { height: 15px; background: var(--bar-block); margin: 10px 0 0; }

/* Q badge, cyan circle */
.badge {
  width: 58px; height: 58px; border-radius: 50%;
  background: var(--accent); color: #fff;
  display: flex; align-items: center; justify-content: center;
  font-family: var(--serif); font-size: 24px; margin: 34px 0 6px;
}
@media (prefers-color-scheme: dark) { .badge { color: #051C34; } }
.badge-sub { font-size: 10px; letter-spacing: .25em; font-weight: 700; margin-bottom: 26px; }

/* cover headline */
.headline {
  font-family: var(--serif); font-weight: 500;
  font-size: clamp(34px, 6.5vw, 54px); line-height: 1.04; letter-spacing: -.005em;
  max-width: 14em; margin: 8px 0 18px;
}
.headline em { font-style: normal; color: var(--accent); }
.standfirst {
  font-size: 16.5px; line-height: 1.6; max-width: 36em; color: var(--ink);
  border-left: 3px solid var(--accent); padding-left: 16px; margin-bottom: 8px;
}

/* summary numbers — the Contents-page cyan numerals */
.numbers {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 26px 34px; margin: 46px 0 8px;
}
.num .n {
  font-family: var(--serif); font-size: 46px; line-height: 1; color: var(--accent);
}
.num .t { font-weight: 700; font-size: 13.5px; margin-top: 6px; }
.num .d { font-size: 12px; color: var(--muted); margin-top: 2px; }

/* section = magazine department opener */
.section { margin-top: 64px; }
.dept {
  font-family: var(--serif); font-size: clamp(28px, 5vw, 38px); font-weight: 500;
  text-align: center; margin: 18px 0 14px;
}
.rule { border: 0; border-top: 1px solid var(--hairline); margin: 0; }
.rule-heavy { border: 0; border-top: 1.5px solid var(--ink); margin: 0; }
.exhibit-label {
  font-size: 10.5px; font-weight: 700; letter-spacing: .2em; text-transform: uppercase;
  color: var(--muted); margin: 26px 0 4px;
}
.exhibit-title { font-weight: 700; font-size: 15.5px; margin-bottom: 20px; }
.exhibit-title em { font-style: italic; font-weight: 400; }

/* bar chart */
.chart { display: grid; grid-template-columns: max-content 1fr max-content; gap: 9px 12px; align-items: center; }
.chart .lbl { font-size: 13px; text-align: right; }
.chart .lbl.artifact { font-weight: 700; }
.chart .track { background: var(--chart-track); height: 20px; }
.chart .bar { height: 100%; background: var(--chart-main); min-width: 2px; }
.chart .bar.artifact { background: var(--chart-artifact); }
.chart .bar.secret { background: var(--chart-secret); }
.chart .val { font-weight: 700; font-size: 14px; font-variant-numeric: tabular-nums; }
.legend { display: flex; gap: 22px; margin-top: 16px; font-size: 11.5px; color: var(--muted); flex-wrap: wrap; }
.legend .k { display: inline-block; width: 11px; height: 11px; margin-right: 6px; vertical-align: -1px; }

/* artifact panel — "what the agent read about itself" */
.panel { background: var(--panel); padding: 26px 26px 20px; margin-top: 26px; }
.panel .arrow {
  display: inline-block; border: 1.5px solid var(--ink); width: 26px; height: 26px;
  text-align: center; line-height: 24px; font-size: 14px; margin-bottom: 14px;
}
.evt-table { width: 100%; border-collapse: collapse; font-size: 12.5px; }
.evt-table th {
  text-align: left; font-size: 10px; letter-spacing: .16em; text-transform: uppercase;
  color: var(--muted); font-weight: 700; padding: 0 10px 8px 0;
  border-bottom: 1px solid var(--hairline);
}
.evt-table td { padding: 7px 10px 7px 0; border-bottom: 1px dotted var(--hairline); vertical-align: top; }
.evt-table td.time { font-variant-numeric: tabular-nums; color: var(--muted); white-space: nowrap; }
.evt-table td.op { font-weight: 700; white-space: nowrap; }
.evt-table td.path { font-family: ui-monospace, "SF Mono", Menlo, monospace; font-size: 11.5px; word-break: break-all; }
.tag {
  font-size: 9.5px; font-weight: 700; letter-spacing: .12em; text-transform: uppercase;
  padding: 2px 7px; white-space: nowrap;
}
.tag.allow { color: var(--ink); border: 1px solid var(--hairline); }
.tag.deny { background: var(--crit); color: #fff; }
.tag.hide { background: var(--chart-secret); color: #fff; }
.tag.critical { background: var(--crit); color: #fff; }
.tag.warning { background: var(--warn); color: #1a1a1a; }
.tag.notice { border: 1px solid var(--accent); color: var(--accent); }
.tag.approved { border: 1px solid var(--chart-secret); color: var(--chart-secret); }

.num-col { text-align: right; font-variant-numeric: tabular-nums; white-space: nowrap; }
th.num-col { text-align: right; }
.denied-num { color: var(--crit); font-weight: 700; }
.cat-table td { padding-right: 14px; }
.totals td { border-top: 1.5px solid var(--ink); border-bottom: none; }
.footnote { font-size: 10.5px; color: var(--muted); margin-top: 14px; line-height: 1.5; }
.footnote b { color: var(--ink); }
.empty { font-size: 13px; color: var(--muted); font-style: italic; padding: 10px 0; }

.endmark { text-align: center; margin-top: 70px; }
.endmark .sq { display: inline-block; width: 10px; height: 10px; background: var(--accent); }
@media print { body { background: #fff; color: #000; } .page { padding: 10px; } }
</style>
</head>
<body>
<div class="page">

  <div class="folio">
    <span>SafeClaude</span>
    <span class="r">Session {{.Session}} _ {{.Quarter}} _ {{.Year}}</span>
  </div>

  <header class="masthead">
    <h1>Session<span class="l2">Report</span></h1>
    <div class="estd"><span>Repo &rsaquo; {{.Repo}}</span><span>{{.Date}}</span></div>
    <div class="blackbar"></div>
  </header>

  <div class="badge">{{.Quarter}}</div>
  <div class="badge-sub">{{.Started}} &middot; {{.Duration}}</div>

  <h2 class="headline">{{.Headline}}</h2>
  <p class="standfirst">{{.Standfirst}}</p>

  <div class="numbers">
    <div class="num"><div class="n">{{.TotalOps}}</div><div class="t">Operations</div><div class="d">audited through the mount</div></div>
    <div class="num"><div class="n">{{.Denials}}</div><div class="t">Denied</div><div class="d">blocked by live policy</div></div>
    <div class="num"><div class="n">{{.Hidden}}</div><div class="t">Hidden</div><div class="d">files kept invisible</div></div>
    <div class="num"><div class="n">{{.ArtifactReads}}</div><div class="t">Agent-file reads</div><div class="d">skills, hooks, memory, config</div></div>
  </div>

  <!-- ================= Exhibit 1 ================= -->
  <section class="section">
    <hr class="rule-heavy">
    <h3 class="dept">Data View</h3>
    <div class="blackbar"></div>

    <p class="exhibit-label">Exhibit 1</p>
    <p class="exhibit-title">Operations by category and outcome, <em>count</em></p>
    <table class="evt-table cat-table">
      <tr><th>Category</th><th class="num-col">Allowed</th><th class="num-col">Denied</th><th class="num-col">Hidden</th><th class="num-col">Total</th><th style="width:28%">Share blocked</th></tr>
      {{range .Cats}}
      <tr>
        <td{{if .Artifact}} style="font-weight:700"{{end}}>{{.Label}}</td>
        <td class="num-col">{{.Allowed}}</td>
        <td class="num-col{{if .Denied}} denied-num{{end}}">{{.Denied}}</td>
        <td class="num-col">{{.Hidden}}</td>
        <td class="num-col" style="font-weight:700">{{.Total}}</td>
        <td><div class="track" style="height:12px"><div class="bar" style="height:100%;background:var(--crit);width:{{printf "%.1f" .DeniedPct}}%"></div></div></td>
      </tr>
      {{end}}
      <tr class="totals">
        <td style="font-weight:700">All categories</td>
        <td class="num-col">{{.TotAllowed}}</td>
        <td class="num-col{{if .TotDenied}} denied-num{{end}}">{{.TotDenied}}</td>
        <td class="num-col">{{.TotHidden}}</td>
        <td class="num-col" style="font-weight:700">{{.TotalOps}}</td>
        <td></td>
      </tr>
    </table>
    <p class="footnote"><b>Note:</b> Bold categories are agent-steering content — anything that changes how Claude Code behaves: skills, hooks, commands, agents, settings, memory files, MCP configuration. "Share blocked" = (denied + hidden) / total for that category.</p>
  </section>

  <!-- ================= Artifact panel ================= -->
  <section class="section">
    <p class="exhibit-label">Exhibit 2</p>
    <p class="exhibit-title">What the agent read about itself</p>
    <div class="panel">
      <span class="arrow">&#8618;</span>
      {{if .ArtifactEvents}}
      <table class="evt-table">
        <tr><th>Time</th><th>Op</th><th>Path</th><th>Outcome</th></tr>
        {{range .ArtifactEvents}}
        <tr>
          <td class="time">{{.Time}}</td>
          <td class="op">{{.Op}}</td>
          <td class="path">{{.Path}}</td>
          <td><span class="tag {{.Action}}">{{.Action}}</span>{{if eq .Severity "critical"}} <span class="tag critical">critical</span>{{end}}</td>
        </tr>
        {{end}}
      </table>
      {{else}}
      <p class="empty">The agent did not touch any skills, hooks, memory, or configuration this session.</p>
      {{end}}
    </div>
    {{if .CriticalWrites}}<p class="footnote"><b>Note:</b> {{.CriticalWrites}} write attempt(s) to agent-steering files are marked critical above — this is the self-modification pattern SafeClaude exists to catch.</p>{{end}}
  </section>

  <!-- ================= Sensitive timeline ================= -->
  <section class="section">
    <p class="exhibit-label">Exhibit 3</p>
    <p class="exhibit-title">Sensitive events, <em>chronological</em></p>
    {{if .Sensitive}}
    <table class="evt-table">
      <tr><th>Time</th><th>Op</th><th>Path</th><th>Severity</th><th>Outcome</th></tr>
      {{range .Sensitive}}
      <tr>
        <td class="time">{{.Time}}</td>
        <td class="op">{{.Op}}</td>
        <td class="path">{{.Path}}</td>
        <td><span class="tag {{.Severity}}">{{.Severity}}</span></td>
        <td><span class="tag {{.Action}}">{{.Action}}</span></td>
      </tr>
      {{end}}
    </table>
    {{else}}
    <p class="empty">No sensitive events this session.</p>
    {{end}}
  </section>

  <!-- ================= Denials ================= -->
  <section class="section">
    <p class="exhibit-label">Exhibit 4</p>
    <p class="exhibit-title">Denials and approvals</p>
    {{if .Denies}}
    <table class="evt-table">
      <tr><th>Path</th><th>Times denied</th><th>Status</th></tr>
      {{range .Denies}}
      <tr>
        <td class="path">{{.Path}}</td>
        <td class="time">{{.Count}}</td>
        <td>{{if .Approved}}<span class="tag approved">approved later</span>{{else}}<span class="tag deny">still blocked</span>{{end}}</td>
      </tr>
      {{end}}
    </table>
    {{else}}
    <p class="empty">Nothing was denied this session.</p>
    {{end}}
    <p class="footnote"><b>Source:</b> SafeClaude audit stream, {{.RepoFull}}. Generated {{.Generated}}. All timestamps local.</p>
  </section>

  <div class="endmark"><span class="sq"></span></div>
</div>
</body>
</html>
`

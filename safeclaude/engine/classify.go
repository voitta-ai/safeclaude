package main

import (
	"path/filepath"
	"strings"
)

// Category tags what kind of artifact a path is. The claude-* categories are
// the point of this tool: they mark files that steer the agent itself
// (skills, hooks, memory, settings), which repos can plant.
type Category string

const (
	CatClaudeSkill    Category = "claude-skill"
	CatClaudeHook     Category = "claude-hook"
	CatClaudeCommand  Category = "claude-command"
	CatClaudeAgent    Category = "claude-agent"
	CatClaudeSettings Category = "claude-settings"
	CatClaudeMemory   Category = "claude-memory"
	CatMCPConfig      Category = "mcp-config"
	CatSecret         Category = "secret"
	// .git split: metadata (HEAD, refs, index, objects) is inert bookkeeping.
	// Hooks are direct code drops (git runs them on commit/checkout).
	// Config is an indirection vector — fsmonitor/hooksPath/pager/aliases
	// make git execute arbitrary commands; writes are as hot as hook writes
	// (fsmonitor fires on a mere `git status`), but reads are routine.
	CatVCSMetadata Category = "vcs-metadata"
	CatVCSHooks    Category = "vcs-hooks"
	CatVCSConfig   Category = "vcs-config"
	CatSource      Category = "source"
)

// AllCategories in display order (reports, menu).
var AllCategories = []Category{
	CatClaudeSkill, CatClaudeHook, CatClaudeCommand, CatClaudeAgent,
	CatClaudeSettings, CatClaudeMemory, CatMCPConfig,
	CatSecret, CatVCSHooks, CatVCSConfig, CatVCSMetadata, CatSource,
}

// IsClaudeArtifact reports whether the category describes agent-steering content.
func (c Category) IsClaudeArtifact() bool {
	return strings.HasPrefix(string(c), "claude-") || c == CatMCPConfig
}

// Label is the human-readable name used in menus and reports.
func (c Category) Label() string {
	switch c {
	case CatClaudeSkill:
		return "Claude skills"
	case CatClaudeHook:
		return "Claude hooks"
	case CatClaudeCommand:
		return "Claude commands"
	case CatClaudeAgent:
		return "Claude agents"
	case CatClaudeSettings:
		return "Claude settings"
	case CatClaudeMemory:
		return "Claude memory"
	case CatMCPConfig:
		return "MCP config"
	case CatSecret:
		return "Secrets"
	case CatVCSMetadata:
		return "Git metadata"
	case CatVCSHooks:
		return "Git hooks"
	case CatVCSConfig:
		return "Git config"
	default:
		return "Source"
	}
}

var secretBases = []string{
	".env*", "*.pem", "*.key", "id_rsa*", "id_ed25519*", "id_ecdsa*",
	"secrets*", "credentials*", ".netrc", "*.p12", "*.pfx", ".htpasswd",
}

// Classify maps a mount-relative path to its Category. First match wins;
// the claude-* rules key off well-known Claude Code repo layout.
func Classify(path string) Category {
	clean := strings.Trim(filepath.ToSlash(filepath.Clean("/"+path)), "/")
	if clean == "" || clean == "." {
		return CatSource
	}
	segs := strings.Split(clean, "/")
	base := segs[len(segs)-1]
	lowBase := strings.ToLower(base)

	// .git anywhere in the path: hooks/ and config are execution vectors,
	// the rest (HEAD, refs, index, objects, logs) is inert metadata.
	for i, s := range segs {
		if s == ".git" {
			rest := segs[i+1:]
			if len(rest) > 0 && rest[0] == "hooks" {
				return CatVCSHooks
			}
			if len(rest) == 1 && rest[0] == "config" {
				return CatVCSConfig
			}
			return CatVCSMetadata
		}
		if s == ".aws" || s == ".ssh" {
			return CatSecret
		}
	}

	// inside a .claude directory (repo-level or nested)
	for i, s := range segs {
		if s != ".claude" {
			continue
		}
		rest := segs[i+1:]
		if len(rest) == 0 {
			return CatClaudeSettings
		}
		switch rest[0] {
		case "skills":
			return CatClaudeSkill
		case "hooks":
			return CatClaudeHook
		case "commands":
			return CatClaudeCommand
		case "agents":
			return CatClaudeAgent
		case "memory":
			return CatClaudeMemory
		default:
			if ok, _ := filepath.Match("settings*.json", rest[0]); ok && len(rest) == 1 {
				return CatClaudeSettings
			}
			return CatClaudeSettings // anything else under .claude steers the agent
		}
	}

	switch base {
	case "CLAUDE.md", "CLAUDE.local.md", "MEMORY.md":
		return CatClaudeMemory
	case "SKILL.md":
		return CatClaudeSkill
	case ".mcp.json", "mcp.json":
		return CatMCPConfig
	}

	for _, pat := range secretBases {
		if ok, _ := filepath.Match(pat, lowBase); ok {
			return CatSecret
		}
	}
	return CatSource
}

// SeverityFor derives event severity from operation, category and action.
// Writes to agent-steering files are always critical: that is the
// hook-injection / self-modification shape.
func SeverityFor(op string, cat Category, action string) string {
	writeOp := op == "WRITE" || op == "CREATE" || op == "DELETE" ||
		op == "RENAME" || op == "CHMOD" || op == "SYMLNK"
	switch {
	case cat.IsClaudeArtifact() && writeOp:
		return "critical"
	case (cat == CatVCSHooks || cat == CatVCSConfig) && writeOp:
		return "critical" // git-hook drop / fsmonitor-hooksPath injection
	case cat == CatVCSHooks || cat == CatVCSConfig:
		return "notice"
	case cat == CatSecret && action != "hide":
		return "warning"
	case cat == CatSecret:
		return "notice"
	case cat.IsClaudeArtifact():
		return "notice"
	default:
		return "info"
	}
}

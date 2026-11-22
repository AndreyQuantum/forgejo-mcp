package tools

import (
	"net/url"
	"strconv"
	"strings"
)

// InstructionsInfo carries the values interpolated into the server
// `instructions` text (section 4.1).
type InstructionsInfo struct {
	Host        string
	Version     string // "" before the first tool call
	DefaultRepo string // "" = none
	Toolsets    map[string]bool
	WaitMax     int // seconds
}

// HostOf returns the host[:port] of an instance URL (the whole string when it
// does not parse).
func HostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// Instructions renders the <= 700 character `instructions` text of section
// 4.1. Sentences about tools in hidden toolsets are omitted.
func Instructions(in InstructionsInfo) string {
	var b strings.Builder
	forgejo := "Forgejo"
	if in.Version != "" {
		forgejo += " " + in.Version
	}
	b.WriteString("forgejo-mcp for " + in.Host + " (" + forgejo + "). ")
	b.WriteString("Every tool: action=help lists its params; unlisted params are reported, not fatal.\n")
	repo := in.DefaultRepo
	if repo == "" {
		repo = `none: pass repo=`
	}
	b.WriteString("Default repo: " + repo + ".\n")
	pulls, actions := in.Toolsets["pulls"], in.Toolsets["actions"]
	switch {
	case pulls && actions:
		b.WriteString("PR flow: pr create -> ci_wait pr=N -> pr merge (when_green=true merges when checks pass). ")
	case pulls:
		b.WriteString("PR flow: pr create -> pr merge. ")
	}
	if actions {
		b.WriteString("CI red: ci_failure pr=N, then run log job=ID grep=...\n")
		b.WriteString("Waits return before " + strconv.Itoa(in.WaitMax) + "s with verdict timeout and a resume line: call the same tool again.\n")
	} else if pulls {
		b.WriteString("\n")
	}
	b.WriteString("Not covered by a tool: forgejo_endpoints q=... then forgejo_api. ")
	b.WriteString("Lists take page/limit; the header shows next. Destructive actions need confirm=<exact target>.")
	return b.String()
}

package tools

import (
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// modeLetter is the one-letter mode used by `help`.
func modeLetter(a Action) string {
	if a.ModeFor != nil {
		return "R/W/D"
	}
	switch a.Mode {
	case Write:
		return "W"
	case Destructive:
		return "D"
	}
	return "R"
}

// HelpText renders the implicit `help` action: the action table of the tool
// (`name [R|W|D] params(required marked *) - summary`) or, when action is
// non-empty, the full parameter list of that one action. Generated from
// Action.Params/Required/Summary, never hand-written twice.
func HelpText(s Spec, allowed []Action, action string) string {
	if action != "" && action != "help" {
		for _, a := range allowed {
			if a.Name == action {
				return actionHelp(s, a)
			}
		}
		names := make([]string, 0, len(allowed))
		for _, a := range allowed {
			names = append(names, a.Name)
		}
		return "error: unknown action " + action + "; valid: " + strings.Join(append(names, "help"), ",")
	}
	var b strings.Builder
	b.WriteString(s.Name + ": " + s.Description + "\n")
	if s.RepoScoped {
		b.WriteString("repo (owner/name) defaults to the detected repository. ")
	}
	b.WriteString("Modes: R read, W write, D destructive (needs confirm). * = required.\n")
	for _, a := range allowed {
		b.WriteString(a.Name + " [" + modeLetter(a) + "]")
		if ps := paramList(a); ps != "" {
			b.WriteString(" " + ps)
		}
		if a.Summary != "" {
			b.WriteString(" - " + a.Summary)
		}
		b.WriteString("\n")
	}
	b.WriteString("help=<action> shows the full parameter list of one action.")
	return b.String()
}

// paramList is `a* b c` (required first, then the other params).
func paramList(a Action) string {
	var out []string
	for _, r := range a.Required {
		out = append(out, r+"*")
	}
	for _, p := range a.Params {
		if !slices.Contains(a.Required, p) {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

type fieldDoc struct{ typ, desc string }

// fieldDocs maps a json param name to its type and description tag text.
func fieldDocs(s Spec) map[string]fieldDoc {
	out := map[string]fieldDoc{}
	t, err := paramsType(s)
	if err != nil {
		return out
	}
	sc, err := jsonschema.ForType(t, nil)
	if err != nil {
		return out
	}
	for name, p := range sc.Properties {
		normalizeNullable(p)
		out[name] = fieldDoc{typ: typeName(p), desc: p.Description}
	}
	return out
}

func typeName(p *jsonschema.Schema) string {
	switch p.Type {
	case "array":
		if p.Items != nil && p.Items.Type != "" {
			return p.Items.Type + "[]"
		}
		return "array"
	case "":
		return "any"
	}
	return p.Type
}

func actionHelp(s Spec, a Action) string {
	docs := fieldDocs(s)
	var b strings.Builder
	b.WriteString(s.Name + "." + a.Name + " [" + modeLetter(a) + "]")
	if a.Summary != "" {
		b.WriteString(" - " + a.Summary)
	}
	b.WriteString("\n")
	line := func(name string, req bool) {
		d := docs[name]
		typ := d.typ
		if typ == "" {
			typ = "any"
		}
		star := ""
		if req {
			star = "*"
		}
		b.WriteString("  " + name + star + " (" + typ + ")")
		if d.desc != "" {
			b.WriteString(" " + d.desc)
		}
		b.WriteString("\n")
	}
	if s.RepoScoped && !a.NoRepo {
		b.WriteString("  repo (string) owner/name; defaults to the detected repository\n")
	}
	for _, r := range a.Required {
		if r != "repo" {
			line(r, true)
		}
	}
	for _, p := range a.Params {
		if !slices.Contains(a.Required, p) && p != "repo" {
			line(p, false)
		}
	}
	if a.Mode == Destructive {
		b.WriteString("  confirm* (string) exact target name\n")
	}
	if a.Needs != "" {
		b.WriteString("needs: " + a.Needs + "\n")
	}
	if a.MinMajor > 0 {
		b.WriteString("min Forgejo major: " + itoa(MinMajorFor(s.Name, a.Name, a.MinMajor)) + "\n")
	}
	for _, e := range a.Endpoints {
		b.WriteString("endpoint: " + e + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

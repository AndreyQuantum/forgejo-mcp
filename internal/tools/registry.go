// Package tools is the tool framework: Spec/Action/Registry, schema
// generation, help, version gating and the Call context (DESIGN.md sections
// 2 and 4). WP0 defines the contracts; WP4 fills the bodies.
package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
)

// Mode classifies an action. Destructive implies Write and requires `confirm`.
type Mode uint8

const (
	Read Mode = iota
	Write
	Destructive
)

// Toolsets lists every toolset name (a tool belongs to exactly one).
// "meta" is always on and cannot be disabled; "admin" is off by default.
var Toolsets = []string{
	"meta", "repos", "pulls", "issues", "actions", "runners", "settings", "users", "packages", "admin",
}

// Action is one value of a tool's `action` enum.
type Action struct {
	Name      string
	Mode      Mode
	Summary   string   // one line, used by `help`
	Params    []string // json names of the params this action reads
	Required  []string // json names; "repo" is added automatically when Spec.RepoScoped and NoRepo is false
	NoRepo    bool
	Endpoints []string // "GET /repos/{owner}/{repo}/pulls" (coverage test + forgejo_endpoints "covered_by")
	MinMajor  int      // 0 = any supported
	Needs     string   // permission text for 403 hints, e.g. "repo admin"
	Hint500   string   // likely causes when the server hides a 500 message
	// ModeFor optionally classifies a call by its params (forgejo_api). The
	// registry keeps such an action when read-only is on and applies ModeFor
	// at call time.
	ModeFor func(in any) Mode
	// Run executes the action; in is a *T where T is the Spec.Params type.
	Run func(c *Call, in any) (*Result, error)
}

// ActionOpt customises an Action built by NewAction.
type ActionOpt func(*Action)

// NewAction builds an Action whose Run receives the decoded *T.
func NewAction[T any](name string, mode Mode, run func(c *Call, p *T) (*Result, error), opts ...ActionOpt) Action {
	a := Action{
		Name: name,
		Mode: mode,
		Run: func(c *Call, in any) (*Result, error) {
			p, ok := in.(*T)
			if !ok {
				return c.Fail("internal: action %s got %T", name, in), nil
			}
			return run(c, p)
		},
	}
	for _, o := range opts {
		o(&a)
	}
	return a
}

// Summary sets Action.Summary.
func Summary(s string) ActionOpt { return func(a *Action) { a.Summary = s } }

// Params sets Action.Params.
func Params(names ...string) ActionOpt { return func(a *Action) { a.Params = names } }

// Required sets Action.Required.
func Required(names ...string) ActionOpt { return func(a *Action) { a.Required = names } }

// NoRepo sets Action.NoRepo.
func NoRepo() ActionOpt { return func(a *Action) { a.NoRepo = true } }

// Endpoints sets Action.Endpoints.
func Endpoints(eps ...string) ActionOpt { return func(a *Action) { a.Endpoints = eps } }

// MinMajor sets Action.MinMajor.
func MinMajor(n int) ActionOpt { return func(a *Action) { a.MinMajor = n } }

// Needs sets Action.Needs.
func Needs(s string) ActionOpt { return func(a *Action) { a.Needs = s } }

// Hint500 sets Action.Hint500.
func Hint500(s string) ActionOpt { return func(a *Action) { a.Hint500 = s } }

// ModeFor sets Action.ModeFor.
func ModeFor(f func(in any) Mode) ActionOpt { return func(a *Action) { a.ModeFor = f } }

// Spec describes one MCP tool.
type Spec struct {
	Name, Toolset, Description string // Description <= 160 chars
	Params                     any    // zero value of the flat params struct (json + jsonschema tags)
	Actions                    []Action
	RepoScoped                 bool // adds `repo` resolution before Run
	// Single marks a single-purpose tool: exactly one Action named "run", no
	// `action` property in the schema, no `help`.
	Single bool
}

// Registry holds every Spec and builds the filtered tool set.
type Registry struct {
	specs []Spec
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{} }

// Add registers a Spec (before Build).
func (r *Registry) Add(s Spec) { r.specs = append(r.specs, s) }

// Specs returns every added Spec (unfiltered).
func (r *Registry) Specs() []Spec { return r.specs }

// ResolveToolsets evaluates a FORGEJO_TOOLSETS value (names, "all", "-name";
// unknown name is an error listing valid names; "meta" is always added).
func ResolveToolsets(spec string) (map[string]bool, error) { panic("WP4") }

// Build applies toolsets, FORGEJO_DENY and read-only (section 4 steps 1-4) and
// computes DeniedEndpoints and SideEffectGets.
func (r *Registry) Build(cfg *config.Config) error { panic("WP4") }

// Enabled returns the filtered Specs (only actions that survived).
func (r *Registry) Enabled() []Spec { panic("WP4") }

// DeniedEndpoints returns the "METHOD template" strings blocked by deny
// rules or disabled toolsets (consumed by forgejo_api policy).
func (r *Registry) DeniedEndpoints() map[string]string { panic("WP4") }

// SideEffectGets returns GET endpoints belonging to non-read actions.
func (r *Registry) SideEffectGets() []string { panic("WP4") }

// CoveredBy returns "tool.action" for an exact "METHOD template", or "".
func (r *Registry) CoveredBy(endpoint string) string { panic("WP4") }

// Install registers every enabled Spec on srv with the raw Server.AddTool
// (handler order of section 2).
func (r *Registry) Install(srv *mcp.Server, env *Env) error { panic("WP4") }

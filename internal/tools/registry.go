// Package tools is the tool framework: Spec/Action/Registry, schema
// generation, help, version gating and the Call context (DESIGN.md sections
// 2 and 4).
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"regexp"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
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

	built     bool
	cfg       *config.Config
	toolsets  map[string]bool
	enabled   []Spec
	reasons   map[string]string // "tool.action" -> disabled reason
	denied    map[string]string // "METHOD template" -> "tool.action"
	sideGets  []string
	covered   map[string]string // "METHOD template" -> "tool.action"
	tokClient sync.Map          // pass-through token -> *forgejo.Client
	tokCount  atomic.Int32
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{} }

// Add registers a Spec (before Build).
func (r *Registry) Add(s Spec) { r.specs = append(r.specs, s) }

// Specs returns every added Spec (unfiltered).
func (r *Registry) Specs() []Spec { return r.specs }

// ResolveToolsets evaluates a FORGEJO_TOOLSETS value (names, "all", "-name";
// unknown name is an error listing valid names; "meta" is always added).
func ResolveToolsets(spec string) (map[string]bool, error) {
	set := map[string]bool{}
	valid := strings.Join(Toolsets, ",")
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		neg := strings.HasPrefix(tok, "-")
		name := strings.TrimPrefix(tok, "-")
		switch {
		case name == "all":
			for _, t := range Toolsets {
				set[t] = !neg
			}
		case slices.Contains(Toolsets, name):
			set[name] = !neg
		default:
			return nil, fmt.Errorf("unknown toolset %q; valid: %s, all", name, valid)
		}
	}
	for k, v := range set {
		if !v {
			delete(set, k)
		}
	}
	set["meta"] = true
	return set, nil
}

// denyMatch reports whether a FORGEJO_DENY entry list blocks tool.action.
func denyMatch(deny []string, tool, action string) bool {
	for _, d := range deny {
		if d == tool+"."+action || d == tool+".*" {
			return true
		}
	}
	return false
}

// Build applies toolsets, FORGEJO_DENY and read-only (section 4 steps 1-4) and
// computes DeniedEndpoints and SideEffectGets.
func (r *Registry) Build(cfg *config.Config) error {
	if cfg == nil {
		cfg = config.Defaults()
	}
	spec := cfg.Toolsets
	if strings.TrimSpace(spec) == "" {
		spec = config.Defaults().Toolsets
	}
	sets, err := ResolveToolsets(spec)
	if err != nil {
		return err
	}
	if err := r.validate(); err != nil {
		return err
	}
	r.cfg, r.toolsets = cfg, sets
	r.enabled = nil
	r.reasons = map[string]string{}
	r.denied = map[string]string{}
	r.covered = map[string]string{}
	gets := map[string]bool{}

	for _, s := range r.specs {
		on := sets[s.Toolset]
		var keep []Action
		for _, a := range s.Actions {
			key := s.Name + "." + a.Name
			for _, ep := range a.Endpoints {
				if _, ok := r.covered[ep]; !ok {
					r.covered[ep] = key
				}
				if a.Mode != Read && strings.HasPrefix(ep, "GET ") {
					gets[ep] = true
				}
			}
			reason := ""
			switch {
			case !on:
				reason = "toolset off"
			case denyMatch(cfg.Deny, s.Name, a.Name):
				reason = "denied"
			case cfg.ReadOnly && a.Mode != Read && a.ModeFor == nil:
				reason = "read-only mode"
			}
			if reason != "" {
				r.reasons[key] = reason
				if reason != "read-only mode" {
					for _, ep := range a.Endpoints {
						if _, ok := r.denied[ep]; !ok {
							r.denied[ep] = key
						}
					}
				}
				continue
			}
			keep = append(keep, a)
		}
		if len(keep) == 0 {
			continue
		}
		c := s
		c.Actions = keep
		r.enabled = append(r.enabled, c)
	}
	r.sideGets = r.sideGets[:0]
	for g := range gets {
		r.sideGets = append(r.sideGets, g)
	}
	sort.Strings(r.sideGets)
	r.built = true
	return nil
}

// validate checks Spec invariants (names, toolsets, Single shape).
func (r *Registry) validate() error {
	seen := map[string]bool{}
	var errs []error
	for _, s := range r.specs {
		switch {
		case s.Name == "":
			errs = append(errs, errors.New("tool without a name"))
			continue
		case seen[s.Name]:
			errs = append(errs, fmt.Errorf("tool %s registered twice", s.Name))
		case !slices.Contains(Toolsets, s.Toolset):
			errs = append(errs, fmt.Errorf("tool %s: unknown toolset %q", s.Name, s.Toolset))
		case utf8.RuneCountInString(s.Description) > BudgetDescriptionChars:
			errs = append(errs, fmt.Errorf("tool %s: description is longer than %d chars", s.Name, BudgetDescriptionChars))
		case s.Single && (len(s.Actions) != 1 || s.Actions[0].Name != "run"):
			errs = append(errs, fmt.Errorf("tool %s: a Single tool needs exactly one action named run", s.Name))
		case len(s.Actions) == 0:
			errs = append(errs, fmt.Errorf("tool %s has no actions", s.Name))
		}
		seen[s.Name] = true
		names := map[string]bool{}
		for _, a := range s.Actions {
			if a.Name == "help" || names[a.Name] || a.Run == nil {
				errs = append(errs, fmt.Errorf("tool %s: bad action %q", s.Name, a.Name))
			}
			names[a.Name] = true
		}
	}
	return errors.Join(errs...)
}

// Enabled returns the filtered Specs (only actions that survived). Before
// Build it returns every Spec.
func (r *Registry) Enabled() []Spec {
	if !r.built {
		return r.specs
	}
	return r.enabled
}

// DeniedEndpoints returns the "METHOD template" strings blocked by deny
// rules or disabled toolsets (consumed by forgejo_api policy); the value is
// the "tool.action" that owns the endpoint.
func (r *Registry) DeniedEndpoints() map[string]string { return r.denied }

// SideEffectGets returns the "GET template" endpoints that belong to non-read
// actions (sorted).
func (r *Registry) SideEffectGets() []string { return r.sideGets }

// CoveredBy returns "tool.action" for an exact "METHOD template", or "".
func (r *Registry) CoveredBy(endpoint string) string {
	if r.covered == nil {
		for _, s := range r.specs {
			for _, a := range s.Actions {
				for _, ep := range a.Endpoints {
					if endpoint == ep {
						return s.Name + "." + a.Name
					}
				}
			}
		}
		return ""
	}
	return r.covered[endpoint]
}

// annotations derives the tool annotations of section 2.
func annotations(s Spec) *mcp.ToolAnnotations {
	open := false
	readOnly, destructive := true, false
	for _, a := range s.Actions {
		if a.Mode != Read || a.ModeFor != nil {
			readOnly = false
		}
		if a.Mode == Destructive || a.ModeFor != nil {
			destructive = true
		}
	}
	if readOnly {
		return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &open}
	}
	return &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &open}
}

// Install registers every enabled Spec on srv with the raw Server.AddTool
// (handler order of section 2).
func (r *Registry) Install(srv *mcp.Server, env *Env) error {
	if !r.built {
		if err := r.Build(env.Cfg); err != nil {
			return err
		}
	}
	for _, s := range r.enabled {
		sc, err := BuildInputSchema(s, s.Actions)
		if err != nil {
			return fmt.Errorf("tool %s: %w", s.Name, err)
		}
		pt, err := paramsType(s)
		if err != nil {
			return err
		}
		h := &toolHandler{reg: r, env: env, spec: r.fullSpec(s.Name), allowed: s.Actions, ptype: pt}
		srv.AddTool(&mcp.Tool{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: sc,
			Annotations: annotations(s),
		}, h.handle)
	}
	return nil
}

func (r *Registry) fullSpec(name string) Spec {
	for _, s := range r.specs {
		if s.Name == name {
			return s
		}
	}
	return Spec{}
}

// toolHandler is the runtime of one registered tool.
type toolHandler struct {
	reg     *Registry
	env     *Env
	spec    Spec     // every action, unfiltered
	allowed []Action // actions that survived filtering
	ptype   reflect.Type
}

var (
	reOwnerRepo = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	stderrLog   = log.New(os.Stderr, "forgejo-mcp: ", log.LstdFlags)
)

// alwaysAccepted are params every call may carry without being "ignored".
var alwaysAccepted = []string{"action", "repo", "help", "confirm", "max_chars"}

const (
	outputReserve = 1500
	hardLimit     = 100000
	fullLimit     = 40000 // Limit used by full=true when no max_chars is given
)

func (h *toolHandler) handle(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	res := h.safeRun(ctx, req)
	if h.env.Cfg != nil && h.env.Cfg.Debug {
		status := "ok"
		if res.IsError {
			status = "error"
		}
		stderrLog.Printf("call %s %s %dms", h.spec.Name, status, time.Since(start).Milliseconds())
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: res.Text}},
		IsError: res.IsError,
	}, nil
}

func (h *toolHandler) safeRun(ctx context.Context, req *mcp.CallToolRequest) (res *Result) {
	env := h.envFor(req)
	defer func() {
		if p := recover(); p != nil {
			msg := fmt.Sprint(p)
			if env.Client != nil {
				msg = env.Client.Redact(msg)
			}
			if env.Cfg != nil && env.Cfg.Debug {
				stderrLog.Printf("panic in %s: %s\n%s", h.spec.Name, msg, debug.Stack())
			}
			res = &Result{Text: "error: internal: " + oneLine(msg), IsError: true}
		}
	}()
	return h.run(ctx, req, env)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

// envFor returns the Env of one request: the shared one, or a copy with a
// per-token client in HTTP pass-through mode (section 3).
func (h *toolHandler) envFor(req *mcp.CallToolRequest) *Env {
	cfg := h.env.Cfg
	if cfg == nil || cfg.Transport != "http" || !cfg.PassThroughToken() || req == nil || req.Extra == nil || req.Extra.Header == nil {
		return h.env
	}
	tok := strings.TrimSpace(req.Extra.Header.Get("X-Forgejo-Token"))
	if tok == "" && cfg.MCPBearer == "" && config.IsLoopbackAddr(cfg.Addr) {
		auth := strings.TrimSpace(req.Extra.Header.Get("Authorization"))
		for _, p := range []string{"Bearer ", "bearer ", "token ", "Token "} {
			if strings.HasPrefix(auth, p) {
				tok = strings.TrimSpace(auth[len(p):])
				break
			}
		}
	}
	if tok == "" {
		return h.env
	}
	e := *h.env
	e.Client = h.reg.clientFor(h.env.Client, tok)
	return &e
}

// clientFor returns the memoised per-token client (bounded to 256 tokens).
func (r *Registry) clientFor(base *forgejo.Client, tok string) *forgejo.Client {
	if v, ok := r.tokClient.Load(tok); ok {
		return v.(*forgejo.Client)
	}
	c := base.WithToken(tok)
	if r.tokCount.Load() < 256 {
		if v, loaded := r.tokClient.LoadOrStore(tok, c); loaded {
			return v.(*forgejo.Client)
		}
		r.tokCount.Add(1)
	}
	return c
}

func fail(format string, a ...any) *Result {
	return &Result{Text: "error: " + fmt.Sprintf(format, a...), IsError: true}
}

func validNames(allowed []Action) string {
	names := make([]string, 0, len(allowed)+1)
	for _, a := range allowed {
		names = append(names, a.Name)
	}
	return strings.Join(append(names, "help"), ",")
}

func jsonKind(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	}
	return "object"
}

// present reports whether args carries a usable value for name.
func present(args map[string]json.RawMessage, name string) bool {
	raw, ok := args[name]
	if !ok {
		return false
	}
	switch strings.TrimSpace(string(raw)) {
	case "", "null", `""`, "[]":
		return false
	}
	return true
}

func (h *toolHandler) run(ctx context.Context, req *mcp.CallToolRequest, env *Env) *Result {
	// (1) decode.
	var raw json.RawMessage
	if req != nil && req.Params != nil {
		raw = req.Params.Arguments
	}
	if s := strings.TrimSpace(string(raw)); s == "" || s == "null" {
		raw = json.RawMessage("{}")
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return fail("invalid: arguments must be a JSON object")
	}
	in := reflect.New(h.ptype).Interface()
	if err := json.Unmarshal(raw, in); err != nil {
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) {
			field := ute.Field
			if i := strings.Index(field, "."); i >= 0 {
				field = field[:i]
			}
			if field == "" {
				field = "?"
			}
			return fail("invalid: param %s: expected %s", field, jsonKind(ute.Type))
		}
		return fail("invalid: arguments: %s", oneLine(err.Error()))
	}
	str := func(name string) string {
		var s string
		if v, ok := args[name]; ok {
			_ = json.Unmarshal(v, &s)
		}
		return s
	}

	// (2)+(3) action and the implicit `help`.
	var name string
	if h.spec.Single {
		name = "run"
	} else {
		name = str("action")
		if helpFor := strings.TrimSpace(str("help")); helpFor != "" || name == "help" {
			return &Result{Text: HelpText(h.spec, h.allowed, helpFor)}
		}
		if name == "" {
			return fail("invalid: missing param action; valid: %s", validNames(h.allowed))
		}
	}
	var act *Action
	for i := range h.allowed {
		if h.allowed[i].Name == name {
			act = &h.allowed[i]
			break
		}
	}
	if act == nil {
		if reason, ok := h.reg.reasons[h.spec.Name+"."+name]; ok {
			return fail("action %s is disabled (%s)", name, reason)
		}
		return fail("unknown action %s; valid: %s", name, validNames(h.allowed))
	}

	// (4) defence in depth: deny / read-only re-check. forgejo_api (ModeFor)
	// stays callable in read-only mode; its policy classifies each call.
	mode := act.Mode
	if act.ModeFor != nil {
		mode = act.ModeFor(in)
	}
	if reason := h.reg.reasons[h.spec.Name+"."+name]; reason != "" {
		return fail("action %s is disabled (%s)", name, reason)
	}
	if env.Cfg != nil && env.Cfg.ReadOnly && act.Mode != Read && act.ModeFor == nil {
		return fail("action %s is disabled (read-only mode)", name)
	}

	// (5) required params.
	var missing []string
	for _, r := range act.Required {
		if r != "repo" && !present(args, r) {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		return fail("invalid: missing param %s for %s.%s", strings.Join(missing, ", "), h.spec.Name, name)
	}

	// (6) minimum Forgejo major.
	need := MinMajorFor(h.spec.Name, name, act.MinMajor)
	var serverVer forgejo.Version
	if need > 0 {
		v, err := env.Client.ServerVersion(ctx)
		if err != nil {
			return errorResult(env, act, need, serverVer, err)
		}
		serverVer = v
		if msg := CheckMinVersion(h.spec.Name, name, act.MinMajor, v); msg != "" {
			return fail("%s", msg)
		}
	}

	// (7) repository.
	var owner, repo string
	if h.spec.RepoScoped && !act.NoRepo {
		if env.Repo == nil {
			return fail("invalid: no repository resolver configured")
		}
		o, n, _, err := env.Repo.Resolve(str("repo"))
		if err != nil {
			return fail("%s", redact(env, err.Error()))
		}
		if !reOwnerRepo.MatchString(o) || !reOwnerRepo.MatchString(n) {
			return fail("invalid: repo %q (owner and name allow letters, digits, . _ -)", o+"/"+n)
		}
		owner, repo = o, n
	}

	// (8) confirm for destructive actions (the exact target is checked by the
	// action through Call.Confirm; forgejo_api decides by canonical path).
	if mode == Destructive && act.ModeFor == nil && strings.TrimSpace(str("confirm")) == "" {
		return fail(`destructive: pass confirm="<exact target>" (see help=%s)`, name)
	}

	// (9) limit and run.
	c := &Call{
		Ctx:      ctx,
		Limit:    limitFor(env, args),
		Env:      env,
		Action:   name,
		Owner:    owner,
		Repo:     repo,
		Progress: progressFunc(ctx, req),
	}
	res, err := act.Run(c, in)
	switch {
	case err != nil:
		res = errorResult(env, act, need, serverVer, err)
	case res == nil:
		res = fail("internal: action %s.%s returned no result", h.spec.Name, name)
	}
	res.Text = redact(env, res.Text)
	if utf8.RuneCountInString(res.Text) > c.Limit {
		res.Text = c.Cap(res.Text)
	}
	if note := ignoredNote(args, *act); note != "" {
		res.Text = strings.TrimRight(res.Text, "\n") + "\n" + note
	}
	return res
}

// ignoredNote lists params the chosen action does not read (section 7.1).
func ignoredNote(args map[string]json.RawMessage, a Action) string {
	var extra []string
	for k := range args {
		if slices.Contains(alwaysAccepted, k) || slices.Contains(a.Params, k) || slices.Contains(a.Required, k) {
			continue
		}
		extra = append(extra, k)
	}
	if len(extra) == 0 {
		return ""
	}
	sort.Strings(extra)
	return "note: ignored params: " + strings.Join(extra, ",")
}

// limitFor computes Call.Limit (6.6): FORGEJO_MAX_OUTPUT, raised by an
// explicit max_chars (or full=true) up to the ceiling 100000.
func limitFor(env *Env, args map[string]json.RawMessage) int {
	base := 16000
	if env.Cfg != nil && env.Cfg.MaxOutput > 0 {
		base = env.Cfg.MaxOutput
	}
	want := 0
	if v, ok := args["max_chars"]; ok {
		var n float64
		if json.Unmarshal(v, &n) == nil && n > 0 {
			want = int(n) + outputReserve
		}
	}
	if v, ok := args["full"]; ok {
		var b bool
		if json.Unmarshal(v, &b) == nil && b && want < fullLimit {
			want = fullLimit
		}
	}
	if want == 0 {
		return min(base, hardLimit)
	}
	return min(hardLimit, max(base, want))
}

// errorResult maps an action error to its Result (section 11).
func errorResult(env *Env, a *Action, need int, v forgejo.Version, err error) *Result {
	var ae *forgejo.APIError
	if errors.As(err, &ae) {
		ann := forgejo.Annotation{Needs: a.Needs, Hint500: a.Hint500}
		if need > 0 && v.Major > 0 && !v.Atleast(need) {
			ann.MinMajorGap, ann.ServerVersion = true, v.String()
		}
		ae.Annotate(ann)
		return &Result{Text: redact(env, ae.Error()), IsError: true}
	}
	if errors.Is(err, context.Canceled) {
		return &Result{Text: "error: canceled", IsError: true}
	}
	return &Result{Text: redact(env, "error: "+oneLine(err.Error())), IsError: true}
}

func redact(env *Env, s string) string {
	if env != nil && env.Client != nil {
		return env.Client.Redact(s)
	}
	return s
}

// EnabledToolsets returns the toolsets selected by the last Build (nil before).
func (r *Registry) EnabledToolsets() map[string]bool { return r.toolsets }

# forgejo-mcp: implementation design

Status: v1 design (revision 2, review fixes applied; see Appendix A), 2026-11-22. Target server: Forgejo v17.0.2 (any instance; nothing is specific to one host).
Repo: `github.com/AndreyQuantum/forgejo-mcp` (public, MIT). Go module path `github.com/AndreyQuantum/forgejo-mcp`.
Research inputs (read them for endpoint-level gotchas; this file only repeats what implementers must act on):
`docs/research/{existing-mcp-survey,api-actions,api-pulls,api-issues,api-repos,api-platform}.md`.
Notation used below: `R` = `/repos/{owner}/{repo}`; `[v]` = verify on a live v17.0.2 instance during integration (the research marks these items unverified).

## 0. Goals, non-goals, decisions

Goal: let an LLM do everything on a Forgejo server over HTTP with the fewest tokens: small tool schemas, compact text output, server-side polling and log processing, no raw API objects.

Fixed decisions (from the brief, not to be reopened):
- Go, one static binary (`CGO_ENABLED=0`), official SDK `github.com/modelcontextprotocol/go-sdk` pinned at **v1.8.0** (latest stable on 2026-11-22; API checked: `mcp.NewServer`, raw `Server.AddTool` + `ToolHandler` (the generic `AddTool[In,Out]` is deliberately not used: it validates arguments against the schema before our handler runs and would replace the error texts of section 11), `ServerSession.NotifyProgress`, `mcp.StdioTransport`, `mcp.NewStreamableHTTPHandler`, `Server.RemoveTools`). stdio mandatory, streamable HTTP behind a flag.
- 30 tools (target 20-30), consolidated by an `action` enum. Toolsets by env. Read-only mode by env. Short descriptions.
- Output is compact plain text, never raw JSON (exception: `forgejo_api` returns compact JSON because it is a raw escape hatch).
- Everything not covered by a typed tool is reachable through `forgejo_api` + `forgejo_endpoints` (embedded index of all 512 operations of the v17.0.2 swagger).
- No third-party code reuse (goern/forgejo-mcp is GPL-3.0). Dependencies: the SDK (and its transitive `jsonschema-go`), `golang.org/x/sync`, `gopkg.in/yaml.v3` (workflow input parsing). Nothing else without a reason in the PR.
- Minimum Go in `go.mod`: `go 1.25.0` (the SDK v1.8.0 itself declares `go 1.25.0`; raise only if another dependency needs more at pin time). CI builds and tests with `go-version: stable` plus one job on the go.mod minimum.

Non-goals: OAuth flows, multi-instance profiles, MCP resources/prompts (tools only), a CLI mode, ActivityPub, git operations on disk (no clone/push; all through HTTP API), password-based login.

Supported servers: Forgejo >= 15 best effort, 17.x tested. Per-action minimum majors are in `internal/tools/minver.go` (section 5.4).

## 1. Package layout

```
forgejo-mcp/
  .gitattributes                   `* text=auto eol=lf` and `**/testdata/** -text` (golden files and recorded fixtures keep LF/CR bytes on Windows)
  cmd/forgejo-mcp/main.go          flags, config load, wiring, stdio|http run loop, --version (falls back to debug.ReadBuildInfo().Main.Version), --check
  internal/clock/clock.go          Clock interface {Now() time.Time; After(d) <-chan time.Time; Sleep(ctx, d) error}, Real(), Fake (advance manually); stdlib only
  internal/config/                 env + flag parsing, validation (config.go, tokenfile.go, config_test.go)
  internal/gitremote/              cwd -> owner/name detection (gitremote.go, parse.go, *_test.go)
  internal/forgejo/                HTTP client and API-level helpers
      client.go                    Client, Request, Response, Do, Download, retries, redirects
      errors.go                    APIError, status->message mapping (section 11)
      paths.go                     Seg, Path, Wild (escaping)
      page.go                      Page, Total/has_more computation, limit clamp
      version.go                   /version parse, Version type, Atleast
      settings.go                  /settings/api cache (default/max limit)
      cache.go                     TTL cache (labels, milestones, repo meta, whoami), keys namespaced by token hash
      types.go                     typed structs for the projected API objects (only fields we use)
  internal/format/                 pure functions, no I/O
      text.go                      Builder (KV, Line), Trunc, OneLine, Footer, ListHeader
      time.go                      T(), Dur(), Age()
      log.go                       job-log cleaning: strip timestamp/ANSI/CR, dedupe, tail, grep, error-marker scan
      diff.go                      diff splitter/filter/cap (section 8.4)
      pick.go                      jq-lite path projection for forgejo_api (section 9)
      noise.go                     generic JSON noise stripping for forgejo_api
  internal/endpoints/
      index.json                   embedded, checked in, generated (WP0 commits the placeholder {"v":"","n":0,"swagger_sha256":"","ops":[]})
      endpoints.go                 //go:embed, Search(), Lookup(), Match(method, path) (section 9.5)
      scope/scope.go               scope-category derivation; no embed, no other internal imports (shared by gen and runtime, so the very first `go generate` works)
      gen/main.go                  swagger.json -> index.json (//go:generate in endpoints.go)
  internal/testutil/               golden.go, env.go (NewEnv), call.go (CallTool), fixtures.go (signatures frozen by WP0, bodies by WP4; section 12.1)
  internal/tools/                  framework
      registry.go                  Spec, Action, Mode, Registry, Register(), toolset+readonly+deny filtering
      schema.go                    input schema build (jsonschema-go + dynamic action enum + per-action property prefixes)
      call.go                      Call, Env, Result, repo resolution, progress, Cap/CapTail, Memo
      help.go                      implicit `help` action
      instructions.go              server `instructions` text generator (section 4.1)
      minver.go                    per-action minimum Forgejo major
      budget.go                    frozen schema-size budget constants (WP4 measures, then freezes)
      all/all.go                   imports every area, `func RegisterAll(r *Registry)`
      shared/                      (WP6) helpers used by several areas: repo meta, label/milestone resolvers, ref normaliser, ResolveTarget, run resolver, AllReviews, ParseOwnerRepo, AssetPath (5.7), OpenLocal/SaveFile (file safety, 10.5)
      shared/ci/                   (WP6b) CI plumbing used by pr, checks, run, ci_failure, ci_wait: CombinedStatus, FetchJobLog, run/job line renderers, FailureDigest, wait/ (Poll, WaitRun, WaitTarget, verdict rules)
      meta/                        forgejo_info, forgejo_api, forgejo_endpoints
      repos/                       repo, branch, tag, wiki
      content/                     release, file, commit
      pulls/                       pr, review
      issues/                      issue, comment, label, milestone, attach
      actions/                     run (incl. workflows, artifacts, log)
      ci/                          ci_failure, ci_wait, checks, actions_config (thin tool wrappers over shared/ci)
      runners/                     runner
      settings/                    hook, access
      users/                       user, org, notify
      packages/                    package
      admin/                       admin
      docgen/                      docs/TOOLS.md generator (WP18)
  test/integration/                docker compose, bootstrap script, harness_test.go (signatures frozen in WP0), Go tests (section 12)
  testdata/                        recorded API fixtures + golden outputs (per package: internal/<pkg>/testdata)
  .github/workflows/{ci.yml,release.yml}
  .goreleaser.yaml  .golangci.yml  Dockerfile  Makefile  server.json  LICENSE  README.md  CHANGELOG.md
  docs/DESIGN.md  docs/research/*
```

Dependency rule: `clock`, `format`, `endpoints` (and `endpoints/scope`), `gitremote`, `config` import nothing internal except stdlib (and `clock`). `forgejo` imports `clock` and `config` types only through plain args. `shared` and `shared/ci` import `forgejo`, `format`, `clock`, `gitremote`, `endpoints` (never `tools`, so there is no cycle; they take a `*forgejo.Client` and plain arguments). `tools` imports `forgejo`, `format`, `endpoints`, `gitremote`, `config`, `clock`. Area packages import `tools`, `forgejo`, `format`, `shared`, `shared/ci`, `clock` only; areas never import each other. Every area package is owned by exactly one work package (section 14), so no two parallel agents ever write the same Go package.

## 2. Frozen core contracts (write these first; area work packages code against them)

```go
// ---- internal/forgejo ----
type Client struct { /* base URL, token, http.Client, ua, sudo, version cache, limits cache */ }
func New(cfg Config) *Client                         // Config{BaseURL, Token, Timeout, Sudo, CAFile, InsecureTLS, UserAgent, HTTPClient *http.Client /*tests inject a Transport*/, Clock clock.Clock /*nil = real; retries/backoff sleep through it*/}
func (c *Client) WithToken(tok string) *Client       // per-request token (HTTP pass-through): shares transport + the `version` and `settings/api` caches only; every other cache is namespaced by sha256(tok)[:8]; redaction list = configured token + tok
type Request struct {
    Method, Path string          // Path relative to /api/v1, e.g. "/repos/o/r/pulls"; ALREADY escaped (use Path()/Seg()); the client builds the URL with url.URL{Path: unescaped, RawPath: Path} so it is never escaped twice
    Query        url.Values
    JSON         any             // marshalled, sets Content-Type: application/json
    Body         io.Reader       // raw body (file upload); with ContentType
    ContentType  string
    Header       http.Header     // extra, e.g. Range
    Timeout      time.Duration   // override (default cfg.Timeout)
    MaxBody      int64           // default 8 MiB; excess is dropped and Response.Truncated=true
    API          string          // "" = /api/v1, "forgejo" = /api/forgejo/v1, "root" = no prefix (web routes like /attachments)
}
type Response struct {
    Status int; Header http.Header; Body []byte; Truncated bool
    Total int64          // X-Total-Count, or -1 when absent
}
func (c *Client) Do(ctx context.Context, r Request) (*Response, error)             // 2xx -> nil error; else *APIError
func (c *Client) Download(ctx context.Context, r Request, w io.Writer) (n int64, resp *Response, err error) // GET only, streams the body (no MaxBody); follows up to 3 redirects INCLUDING cross-host ones (MinIO/S3 presigned URLs), dropping Authorization and Sudo on every hop whose host differs from the instance host; never sends credentials to a URL taken from a response body (section 5.7)
func (c *Client) GetJSON(ctx context.Context, path string, q url.Values, out any) (*Response, error)
func (c *Client) SendJSON(ctx context.Context, method, path string, q url.Values, in, out any) (*Response, error) // out may be nil; 204/empty body ok
func (c *Client) ServerVersion(ctx context.Context) (Version, error)               // cached for process lifetime
func (c *Client) Limits(ctx context.Context) (def, max int)                        // /settings/api, cached; falls back to 30/50
func (c *Client) Me(ctx context.Context) (*User, error)                            // cached 10 min
type APIError struct { Status int; Message string; Method, Path string; Body string; Hint string }
func (e *APIError) Error() string                                                  // compact one-liner (section 11)
func Seg(s string) string                  // url.PathEscape one segment ('/' becomes %2F)
func EscapeKeepPct(s string) string        // forgejo_api only: escapes per segment keeping '/', but leaves valid %XX sequences as given (no %20 -> %2520)
func Path(parts ...string) string          // "/" + escaped parts joined; a part wrapped by Wild() keeps its '/'
func Wild(s string) string                 // marks a multi-segment part (file path, branch with '/')
type Page struct { Page, Limit int; Total int64 /*-1 unknown*/; Shown int; More bool }
func PageFrom(resp *Response, page, limit, shown int) Page   // shown = RAW item count before any client-side filtering; More = page*limit < Total, else shown==limit (also the rule for lists with an unreliable total, 6.7)
func (c *Client) List(ctx context.Context, path string, q url.Values, page, limit int, out any) (Page, error) // sets page/limit, clamps limit to server max
func (c *Client) Cache() *Cache            // Cache.Get(key, ttl, loader func()(any,error)) (any, error); Cache.Invalidate(prefix)

// ---- internal/format ----
type B struct{ /* strings.Builder wrapper */ }
func (b *B) KV(key string, val any)               // "key: val"\n ; skips zero values; multi-line values indented 2
func (b *B) Line(parts ...string)                 // joins non-empty parts with " | "
func (b *B) Raw(s string)
func (b *B) String() string
func ListHeader(kind string, p Page) string       // "# prs 20/134 page 1/7 next: page=2" (one form everywhere, no parentheses; unknown total: "# prs 20 page 1 next: page=2")
func Trunc(s string, max int, how string) string  // rune-safe cut + "\n[truncated: N of M chars; <how>]"
func OneLine(s string, max int) string            // collapses whitespace, cuts with "…"
func T(s string) string                           // RFC3339 -> "2006-01-02 15:04" UTC ; "" stays ""
func Dur(d time.Duration) string                  // 63s -> "1m03s"
func SHA(s string, n int) string
func Tags(xs []string) string                     // "[a,b]" or ""
func Logins(xs []string) string                   // "@a,@b" or ""

// ---- internal/tools ----
type Mode uint8
const ( Read Mode = iota; Write; Destructive )   // Destructive implies Write and requires `confirm`
type Action struct {
    Name      string
    Mode      Mode
    Summary   string       // one line, used by `help`
    Params    []string     // json names of the params this action reads (for `help` and unknown-param warnings)
    Required  []string     // json names; "repo" is added automatically when Spec.RepoScoped and Action.NoRepo is false
    NoRepo    bool
    Endpoints []string     // "GET /repos/{owner}/{repo}/pulls"  (used by coverage test + forgejo_endpoints "covered_by")
    MinMajor  int          // 0 = any supported
    Needs     string       // permission text used in 403 hints, e.g. "repo admin"
    Hint500   string       // likely causes when the server hides a 500 message
    ModeFor   func(in any) Mode // optional: classifies a call by its params (forgejo_api: GET/HEAD read, other methods write, DELETE on destructive templates destructive). The registry keeps such an action when read-only is on and applies ModeFor at call time, so forgejo_api stays registered
    Run       func(c *Call, in any) (*Result, error)  // built with NewAction[T]
}
func NewAction[T any](name string, mode Mode, run func(c *Call, p *T) (*Result, error), opts ...ActionOpt) Action
type Spec struct {
    Name, Toolset, Description string   // Description <= 160 chars
    Params     any                      // zero value of the flat params struct (json + jsonschema tags)
    Actions    []Action
    RepoScoped bool                     // adds `repo` resolution before Run
    Single     bool                     // single-purpose tool: exactly one Action (named "run"), no `action` property in the schema, no `help` (forgejo_info, forgejo_api, forgejo_endpoints, ci_failure, ci_wait)
}
type Registry struct{ /* filtered by toolsets, read-only, deny list */ }
func (r *Registry) Add(s Spec)
type Env struct { Client *forgejo.Client; Cfg *config.Config; Repo *gitremote.Detector; Clock clock.Clock; Memo *Memo /* in-process facts shared between tools, e.g. Memo.Set("automerge:o/r#42") after a 201 from pr merge */ }
type Memo struct{ /* mutex + set */ }
func (m *Memo) Set(key string)
func (m *Memo) Has(key string) bool
type Call struct {
    Ctx      context.Context
    Limit    int                          // effective output cap for this call (6.6): FORGEJO_MAX_OUTPUT, raised by an explicit max_chars up to the hard ceiling 100000
    Env      *Env
    Action   string
    Owner, Repo string                 // resolved when RepoScoped (empty otherwise)
    Progress func(msg string, done, total float64) // no-op when the client sent no progress token
}
type Result struct { Text string; IsError bool }  // Text already capped by Call.Cap()/CapTail()
func (c *Call) Cap(s string) string               // last-resort cap to Call.Limit, head-keeping, cut at a line boundary; trailer lines (those starting with `[truncated:`, `next_offset:`, `resume:`) are re-appended after the cut so they always survive
func (c *Call) CapTail(s string) string           // same, but keeps the END and cuts the beginning (log tails, ci_failure, ci_wait failure digests); marker first
func (c *Call) Chars(requested, def int) int      // effective per-action size: requested (or def) clamped to [200, Limit-1500]; see 6.6
func (c *Call) Fail(format string, a ...any) *Result
```

Each area package exports `func Register(r *tools.Registry)`; `internal/tools/all/all.go` calls every area's `Register`. WP0 creates all area packages as stubs (`func Register(*tools.Registry) {}`) so area work packages edit only their own directory.

SDK glue (in `registry.go`): every enabled Spec is registered with the raw `server.AddTool(&mcp.Tool{Name, Description, InputSchema, Annotations}, handler)` where `handler(ctx, *mcp.CallToolRequest)` decodes `req.Params.Arguments` itself (the SDK does no schema validation for the raw method, so our messages from section 11 are what the model sees). We return only `Content: []mcp.Content{&mcp.TextContent{Text}}`, no StructuredContent, so nothing is duplicated. `InputSchema` is built by `schema.go`: infer with `jsonschema.For[T](nil)` from `Spec.Params`, then replace `properties.action` with `{type:string, enum:[allowed actions + "help"]}`, mark only `action` required, set `additionalProperties:false`. Free-form object params (`inputs`, `set`, `query`, `body`, `units_map`) are declared `map[string]any` (never `map[string]string`) so a JSON `true` or number is accepted and stringified by the action. Property descriptions: `[<actions that read it>] <text <= 12 words>` generated from `Action.Params`/`Action.Required` with `*` after the actions that require it, e.g. `n: [get*,merge*,edit*,files*,diff*] PR number` (properties used by every action, `action` and `repo`, carry no prefix; self-explanatory `n`, `page`, `limit` may omit the text). The prefix is how the model learns which params an action needs without an `action=help` round trip. Annotations: `ReadOnlyHint=true` when the (filtered) tool has only Read actions; otherwise `ReadOnlyHint=false` and `DestructiveHint` is an explicit pointer: `true` for tools containing a Destructive action, `false` for every other tool with Write actions (MCP treats an unset destructiveHint as true); `OpenWorldHint=false`.

Handler order: (1) decode arguments into the Params struct (`json.Unmarshal`, unknown keys kept for the warning of 7.1; a type mismatch -> `error: invalid: param <name>: expected <type>`); (2) resolve action (unknown -> `error: unknown action X; valid: a,b,c`); (3) `help` -> `help.go`; (4) deny/read-only re-check (defence in depth even though hidden; for `ModeFor` actions the mode of this call): `error: action X is disabled (read-only mode|denied|toolset off)`; (5) required-param check; (6) min-version check (`ServerVersion`); (7) repo resolution; (8) `confirm` check for Destructive; (9) compute `Call.Limit` (6.6) and `Run`; (10) APIError -> `Result{IsError:true}` per section 11; panics are recovered and reported as `error: internal: ...`.

## 3. Configuration

Env (all optional except URL):

| Var | Meaning | Default |
|---|---|---|
| `FORGEJO_URL` | instance web root, e.g. `https://forgejo.example.com` (a trailing `/`, `/api/v1` and `.git` are stripped; sub-path installs allowed) | required |
| `FORGEJO_TOKEN` | personal access token | none (anonymous, read-only on public repos) |
| `FORGEJO_TOKEN_FILE` | path to a file holding the token. Used only when `FORGEJO_TOKEN` is empty. The file is read as bytes: a UTF-8 BOM is stripped, a UTF-16 LE/BE BOM is detected and decoded (PowerShell 5.1 `echo tok > file` writes UTF-16LE), then whitespace/CR/LF are trimmed; a token that still contains non-printable or non-ASCII bytes is a startup error naming the file but never its content | none |
| `FORGEJO_REPO` | default repo `owner/name` | auto-detect |
| `FORGEJO_SSH_HOSTS` | comma list of extra hosts (`host` or `host:port`) that mean "this instance" in git remotes | none |
| `FORGEJO_DIR` | directory to start repo detection from | process cwd |
| `FORGEJO_TOOLSETS` | comma list: names, `all`, `-name` to subtract | `repos,pulls,issues,actions,runners,settings,users,packages` (`admin` off) |
| `FORGEJO_READ_ONLY` | `1/true` hides and blocks mutating actions | false |
| `FORGEJO_DENY` | comma list of `tool.action` or `tool.*` hard-blocked and hidden (e.g. `repo.delete,admin.*`). It also blocks the matching endpoints in `forgejo_api` (section 4 step 5), so it is a real control | none |
| `FORGEJO_TIMEOUT` | per-request HTTP timeout | `30s` |
| `FORGEJO_MAX_OUTPUT` | char cap per tool result; a call that passes an explicit `max_chars`/`full=true` may raise its own cap up to the hard ceiling 100000 (6.6) | `16000` |
| `FORGEJO_DEFAULT_LIMIT` | default list `limit` (clamped to server max) | `20` |
| `FORGEJO_WAIT_DEFAULT` | default `ci_wait` timeout seconds (must stay below the MCP client's tool timeout, often 60 s) | `50` |
| `FORGEJO_WAIT_MAX` | hard cap for `ci_wait` timeout seconds; raise it (up to 600) only for clients with a long tool timeout (Claude Code: set `MCP_TOOL_TIMEOUT` too) | `55` |
| `FORGEJO_SAVE_DIR` | the only root for local file params (`save_to`, `file`, section 10.5). Created 0700 on first use | `os.TempDir()/forgejo-mcp` |
| `FORGEJO_SAVE_CWD` | `1` additionally allows the process cwd as a root (still never dot-components, see 10.5) | off |
| `FORGEJO_HTTP_FILES` | `1` enables local file params in HTTP mode (they are disabled there by default) | off |
| `FORGEJO_SUDO` | username sent as `Sudo:` header (site admin tokens only) | none |
| `FORGEJO_CA_FILE` / `FORGEJO_TLS_INSECURE` | extra CA bundle / skip TLS verify (`1`) | none |
| `FORGEJO_MCP_BEARER` | HTTP mode: required bearer for non-loopback listen; when set, per-request Forgejo tokens must come in `X-Forgejo-Token` | none |
| `FORGEJO_DEBUG` | `1` logs requests (method, path, status, ms; never headers/bodies) to stderr | off |

Flags (override env): `--transport stdio|http` (default stdio), `--addr 127.0.0.1:8787`, `--toolsets`, `--read-only`, `--version`, `--check` (loads config, calls `/version` + `/user`, prints one summary and exits 0/1), `--help`.

Rules: validation errors are printed to stderr and exit 2 (stdout is reserved for the MCP stream; nothing else may write to stdout). `FORGEJO_URL` must parse with scheme http/https and a host. A missing token is a warning, not an error. Token value is never logged or echoed (config `String()` redacts). `FORGEJO_WAIT_DEFAULT <= FORGEJO_WAIT_MAX <= 600` is validated. HTTP mode: default bind `127.0.0.1:8787`, path `/mcp`; a non-loopback `--addr` requires `FORGEJO_MCP_BEARER` (constant-time compare on `Authorization: Bearer`). Per-request Forgejo token (pass-through, only when `FORGEJO_TOKEN` and `FORGEJO_TOKEN_FILE` are empty): taken from the header `X-Forgejo-Token` (read from `req.Extra.Header` in the handler); the `Authorization` header (`Bearer <tok>` or `token <tok>`) is accepted as the Forgejo token only on a loopback bind with no `FORGEJO_MCP_BEARER`; when a bearer is configured, `Authorization` is never forwarded to Forgejo. Each pass-through token gets its own `Client.WithToken` client (separate caches and redaction, section 5.5). Neither header value is ever logged. `http.Server{ReadHeaderTimeout: 10s, IdleTimeout: 120s}`, no `WriteTimeout` (long SSE responses of `ci_wait` would be cut); handler `mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {return srv}, &mcp.StreamableHTTPOptions{PropagateRequestCancellation: true, SessionTimeout: 30 * time.Minute})` (option names `[v]`, 15.10) so a client cancel stops our polling. Local file params are off in HTTP mode unless `FORGEJO_HTTP_FILES=1`.

### 3.1 Default repo resolution (`internal/gitremote`)

`Detector.Resolve(explicit string) (owner, name string, src string, err error)`; `src` in `param|env|git:<remote>`.

1. If the call has `repo`: accept `owner/name`; also accept a pasted web URL (`https://host/owner/name[.git][/...]`, host must equal the instance host) and `name` alone (owner taken from the default repo's owner, error if none). Strip a trailing `.git`.
2. Else if `FORGEJO_REPO` is set: it must be `owner/name` (validated at startup).
3. Else auto-detect (cached; re-run at most once per 30 s after a failure; successful result cached for process life):
   a. Start dir = `FORGEJO_DIR` or `os.Getwd()`. Walk up until a `.git` entry exists. If `.git` is a file (`gitdir: <path>`), follow it; for a linked worktree read `<gitdir>/commondir` and use the common dir's `config`.
   b. Parse `config` (INI subset: `[remote "name"]` sections with `url =`, `[url "base"]` sections with `insteadOf =`/`pushInsteadOf =`). Apply insteadOf (longest prefix wins) before matching. No `git` binary is executed.
   c. Candidate remotes ordered: `origin`, `upstream`, then others alphabetically.
   d. Parse each URL: `https|http://[user[:pw]@]host[:port]/path`, `ssh://[user@]host[:port]/path`, `git://host/path`, scp-like `[user@]host:path` (no `://`, colon before first slash). Credentials in the URL are discarded and never logged.
   e. Host matches when: hostname (case-insensitive) equals the hostname of `FORGEJO_URL`; or equals the hostname of any `FORGEJO_SSH_HOSTS` entry (when the entry has `:port`, the URL port must equal it; scp-like URLs have no port and match entries without port or any entry with that hostname). For http(s) remotes the instance sub-path prefix (if `FORGEJO_URL` has one) is stripped from the path first.
   f. Path -> `owner/name`: trim leading `/`, trim `.git`, require exactly 2 segments (more -> take the first two only when a sub-path was stripped, else no match).
   g. First matching remote wins. No match: `err` = `no default repo: pass repo=owner/name or set FORGEJO_REPO` (and, if remotes existed, `; remotes seen: origin(host github.com), ...` host names only).
4. Current branch and default target (`shared.ResolveTarget`; used by `ci_wait`, `ci_failure`, `checks get` and `pr get` without `n`, and, for the branch name only, by `pr create` default `head`): read `<gitdir>/HEAD`; `ref: refs/heads/x` -> `x`; detached -> "". It is offered only when the resolved repo equals the git-detected repo. Rule, one for all of those tools: when the current branch exists and differs from the repo's default branch, then (a) if an open PR has it as head (`GET R/pulls?state=open&head=<branch>&limit=5`, take the one whose head repo is this repo) the target is that PR; (b) otherwise the target is the branch head (`ref=<branch>`). In every other case (no git context, detached, default branch, other repo) the target is the repo's default branch. Explicit `pr`/`run`/`number`/`ref`/`sha`/`n` params always win. Every tool using it prints the chosen target on its first line: `target: pr #42 (branch fix/login)` or `target: branch main (default)`.

Fuzz target `FuzzParseRemoteURL`; table tests include: GitHub-style scp, `ssh://git@git-ssh.example.com:2222/o/r.git` with `FORGEJO_SSH_HOSTS=git-ssh.example.com:2222`, worktree `.git` file, `insteadOf`, sub-path instance, uppercase host, trailing slash, remote with credentials.

## 4. Toolsets, read-only, deny

Toolsets (a tool belongs to exactly one): `meta` (always on, cannot be disabled), `repos`, `pulls`, `issues`, `actions`, `runners`, `settings`, `users`, `packages`, `admin`.

Filtering algorithm at startup (`Registry.Build`):
1. Enabled set from `FORGEJO_TOOLSETS` (left to right; `all` = every toolset; `-x` removes; unknown name = startup error listing valid names). `meta` always added.
2. For each Spec in an enabled toolset, drop actions matching `FORGEJO_DENY` (`tool.action`, `tool.*`).
3. If read-only: drop every action with Mode Write or Destructive, except an action that has `ModeFor` (only `forgejo_api`): it stays registered and is classified per call (step 5).
4. A Spec with zero remaining actions is not registered. The `action` enum of registered tools lists only remaining actions (plus `help`), so hidden actions are not visible to the model; a call with a hidden action name returns `error: action X is disabled (read-only mode|denied|toolset off)` (the raw `Server.AddTool` registration of section 2 is what lets this text reach the model).
5. `forgejo_api` policy (`internal/tools/meta/policy.go`, applied to every call; it is what makes deny, toolsets and read-only real controls):
   a. Canonicalise the path: reject absolute URLs, `?`, `#`, `..` and encoded `%2e`/`%2f`/`%5c` sequences; strip a leading `/api/v1` (a pasted full path); collapse `//`; strip a trailing `/`.
   b. Look the op up with `endpoints.Match(method, path)` (segment-wise match on the index templates; `{var}` matches one segment; the last variable of a template the generator flags wild (`filepath`, branch/tag names) matches the remainder). Result: `{operationId, "METHOD template"}` or nil.
   c. Deny: block when the op's `METHOD template` is listed in `Action.Endpoints` of any action matched by `FORGEJO_DENY` or belonging to a disabled toolset, or when the template starts with `/admin/` while toolset `admin` is off (message: `error: forgejo_api: blocked by FORGEJO_DENY|toolset off (<tool.action>)`). `forgejo_api.*` in `FORGEJO_DENY` blocks the tool itself.
   d. Read-only: only `GET`/`HEAD` pass, plus `POST` to `/markdown`, `/markdown/raw`, `/markup` (pure rendering). GETs with side effects are blocked: the registry derives the list at startup from every Action with Mode != Read whose `Endpoints` contain a GET (today `runner.reg_token`: the repo, org, user and admin `.../actions/runners/registration-token` templates) (the registry exports `DeniedEndpoints()` and `SideEffectGets()`, computed once in WP4's `Registry.Build`; `policy.go` in WP5 only consumes them) and always adds the pattern `*/registration-token` (also the legacy `/admin/runners/registration-token`).
   e. Unknown op (nil match): GET/HEAD pass unless step d's pattern hits; non-GET is refused in read-only; when `FORGEJO_DENY` is non-empty or a toolset is off, an unmatched non-GET call is refused (`error: forgejo_api: path not in the endpoint index; deny rules cannot be applied`).
   f. Destructive confirm: a DELETE whose op matches a Destructive action's `Endpoints` (templates for `DELETE /repos/{owner}/{repo}`, `/orgs/{org}`, `/teams/{id}`, `/admin/users/{username}`, `/admin/unadopted/...`, `/packages/...`, `/repos/{owner}/{repo}/issues/{index}`), or, when unmatched, a DELETE on exactly `/repos/{o}/{r}`, `/orgs/{org}`, `/admin/users/{u}` or `/packages/...`, requires `confirm` equal to the canonical path. The decision is made on the canonical path, so `//repos/o/r/` cannot slip past.
6. `forgejo_endpoints` and `forgejo_info` are always read-only and always on.

Destructive actions (require `confirm`): `repo.delete`, `org.delete`, `org.team_delete`, `issue.delete`, `package.delete`, `admin.users_delete`, `admin.unadopted_delete`. All other deletes (branch, tag, release, label, comment, run, ...) are ordinary Write. `confirm` is a string param: for repo/org/issue/package/user it must equal the target name (`owner/name`, `org`, `owner/name#n`, `owner/type/name/version`, `username`); anything else returns `error: destructive: pass confirm="<exact target>"`.

Tool annotations follow section 2.

### 4.1 Server `instructions` text (generated in `internal/tools/instructions.go`, <= 700 chars)

Sent once in the `initialize` result. Sentences about a tool in a hidden toolset are omitted; numbers come from config. Template:
```
forgejo-mcp for <host> (Forgejo <version>). Every tool: action=help lists its params; unlisted params are reported, not fatal.
Default repo: <owner/name or "none: pass repo=">.
PR flow: pr create -> ci_wait pr=N -> pr merge (when_green=true merges when checks pass). CI red: ci_failure pr=N, then run log job=ID grep=...
Waits return before <WAIT_MAX>s with verdict timeout and a resume line: call the same tool again.
Not covered by a tool: forgejo_endpoints q=... then forgejo_api. Lists take page/limit; the header shows next. Destructive actions need confirm=<exact target>.
```
The version line is filled lazily from the cached `/version` (no extra call at init; before the first tool call it prints `Forgejo` only).

## 5. HTTP client behaviour

5.1 Request: `Authorization: token <tok>`, `Accept: application/json`, `User-Agent: forgejo-mcp/<version>`, `Sudo:` if configured, `Content-Type: application/json` only with a body (except the multipart cases named in 8.2/8.5). Never use `?token=` (ignored by v17). URLs are built with `url.URL{Path, RawPath}` from the already-escaped `Request.Path`, so `%20` is never turned into `%2520`. Go transport with gzip, `MaxIdleConnsPerHost 8`, dial timeout 10 s, response header timeout = request timeout.

5.2 Redirects: API calls (`Do`) on GET/HEAD follow up to 3 redirects, same host only, keeping the Authorization header. Non-GET never follows: a 301/307/308 becomes `APIError{Status, Message:"moved: <Location path>"}` with hint `repo or user was renamed; retry with the new name` (a naive replay would turn POST into GET). Streaming downloads (`Client.Download`: release/attachment assets, Actions artifacts, archives) also follow cross-host redirects (up to 3, http/https only), because with MinIO/S3 `ServeDirect` the server answers 302 to a presigned storage URL on another host; on every hop whose host differs from the instance host the `Authorization` and `Sudo` headers are dropped (presigned URLs carry their own signature).

5.3 Retries: GET/HEAD only; up to 2 retries on network error and 502/503/504/429, backoff 300 ms then 900 ms (+-20% jitter); honor `Retry-After` up to 10 s. Mutations are never retried automatically (except the explicit merge retry in section 10.3).

5.4 Version gating: first tool call triggers `GET /version` (parse `^v?(\d+)\.(\d+)\.(\d+)`, ignore suffix like `+gitea-1.22.0`). `Action.MinMajor` is checked before the call: `error: <tool>.<action> needs Forgejo >= N (server is X)`. Table in `minver.go` (values are `[v]`, adjust after integration): job logs + job get + run rerun/cancel/delete + runs list: 16; runner register (POST): 15; dispatch with `return_run_info`: 16 (older: send without, then locate the run by listing `workflow_id` + newest); everything else 0. A server with major < 15 prints a startup warning in `forgejo_info` only. When a typed call gets a generic 404 on an endpoint whose Action.MinMajor > server major, the hint becomes `endpoint may not exist on this Forgejo version`.

5.5 Caches (`forgejo.Cache`, in memory): `version` (process) and `settings/api` (process) are keyed `host|` and shared by all tokens. Everything else is namespaced `host|<tok8>|...` with `tok8` = hex of the first 8 bytes of sha256(token) (`anon` when there is no token): `me` (10 min), `repo:<o>/<n>` meta (5 min: default_branch, empty, archived, merge settings `allow_merge_commits|allow_rebase|allow_rebase_explicit|allow_squash_merge|allow_fast_forward_only_merge` and `default_merge_style`, `default_delete_branch_after_merge`, owner type, permissions; there is no `allow_manual_merge` in the API object, it exists only in the edit options), `labels:<o>/<n>` (repo labels + org labels when owner is an org; 60 s), `milestones:<o>/<n>` (60 s), `assignees` (60 s), and the `file read` ETag cache. Two tokens therefore never share identity, permissions or the label names of private repos (test: two tokens against one fake server). `WithToken` clients carry their own redaction list (configured token + that token). Any successful mutation of labels/milestones/repo edit invalidates its prefix for that token.

5.6 Name resolution (client-side, so the model passes names): `labels` (names) -> ids for `issue create` / `pr create` / `pr edit` via the cached label list (repo labels, plus `GET /orgs/{org}/labels` when owner is an org); unknown name -> `error: unknown label "x"; known: a,b,c (first 30)` (the server would silently drop it). `milestone` accepts title or numeric id -> id via cached list; unknown -> error. Assignees are passed through but pre-validated against `GET R/assignees` only when the server answers 422.

5.7 Download URL rule: credentials are never sent to a URL taken from a response body. Release assets and attachments carry `browser_download_url` (built from the server's ROOT_URL, which can differ from `FORGEJO_URL`, and possibly pointing to any host for `type: external` assets). Rule: (a) an external asset (release asset with `external_url`/`type: external`) is not downloaded; the result is `external asset: <url> (not downloaded; credentials are not sent to other hosts)`; (b) for internal assets only the URL path is used, minus the ROOT_URL sub-path (learned once from the repo's `html_url` minus `/<owner>/<repo>`); the request is rebuilt on `FORGEJO_URL` with `Request{API:"root", Path}` and run through `Client.Download`. Unit test: an asset URL on another host is never requested with the token.

## 6. Output format

6.1 General: UTF-8 plain text. No JSON, no markdown tables, no ANSI. One item per line. ASCII separators ` | `. Field omission: optional fields that are empty are omitted entirely (never `null`, `none`, `-`). Fields are self-describing by shape: `#12` issue/PR number (`#` is reserved for them), `no.56` Actions run number, `by alice`, `[bug,ui]` labels, `@bob,@eve` assignees, `ms:v1.2` milestone, `c:3` comment count, `due:2026-12-01`, `+12 -3` additions/deletions, `upd 2026-11-22 10:15` updated.
6.2 Times: UTC, `2026-11-22 10:15` in lists and objects (seconds only in logs/steps when asked). Durations `1m03s`, `42s`, `2h05m`. Sizes `1.2MB` (1000-based, one decimal). SHAs: 8 chars in lists, 40 in single-object views only when `verbose`. URLs: only `html_url` of the primary object in single views (`url: ...`), never in list lines unless an action needs it (run/job `url` is dropped; model can pass the id back).
6.3 Single object: header line `<kind> <id>` then `key: value` lines, fixed key order per type (section 6.9). Body/long text last under `body:` with the standard truncation marker. Multi-line values are indented two spaces.
6.4 Lists: first line `# <kind> <shown>/<total> page <p>/<pages>` + ` next: page=<p+1>` when more (one form everywhere, no parentheses); when total unknown `# <kind> <shown> page <p>` + ` next: page=<p+1>` when more. Then the lines. No trailing footer (the header carries pagination). Empty list: `# prs 0/0`.
6.5 Mutation results: one line, `ok: <what happened>` plus key identifiers and the new state, e.g. `ok: created pr #43 fix/login->main  https://host/o/r/pulls/43`. Never echo the full object. Multi-step mutations list steps: `ok: merged pr #42 as 8a1b2c3d (branch deleted)`.
6.6 Sizes and truncation:
- One unit for every user-facing size param: **characters** (runes), named `max_chars`. (Server-side byte ranges for logs are requested as `2*max_chars` bytes; the cleaned text is then cut in chars.)
- Marker (exact): `[truncated: <shown> of <total> <unit>; <how to get more>]` e.g. `[truncated: 1500 of 8400 chars; pass body_max=0 or full=true]`. The hint names only params that can actually help (`paths`, `step`, `grep`, `tail`, `page`, `max_chars`), never a param that is already at its ceiling.
- Per-field caps: title 120 chars (`…`), comment bodies in lists 300, issue/PR body 1500 (`body_max`, `0` = up to the hard cap 20000 chars), review body 300, commit subject = first line (cap 120), release body 2000. `full=true` lifts per-field caps to 20000 per field.
- Output cap (`Call.Limit`): `FORGEJO_MAX_OUTPUT` (default 16000) by default. An explicit `max_chars` (or `full=true`) on the call raises it: `Limit = min(100000, max(FORGEJO_MAX_OUTPUT, max_chars + 1500))` (1500 = header + trailer reserve). So the hint "raise max_chars" really works up to 100000.
- Defaults: each action's own default is clamped to `Limit - 1500` (`Call.Chars`): file read 12000, diff 14000, log 8000, `forgejo_api` 12000; hard ceiling for the params 100000 (so `help` prints the effective range).
- Renderers budget themselves: `max_chars` applies to the body part; header lines and trailer lines (diff omitted-files trailer, log truncation marker, `next_offset:`, `resume:`) are added outside that budget and always survive. `Call.Cap` is only the last resort (result still above `Limit`): it cuts at a line boundary, appends `[truncated: output cut at N chars, M lines omitted; narrow with limit/path/q]` and re-appends any trailer lines (prefix `[truncated:`, `next_offset:`, `resume:`).
- Tail-oriented output (log tail, `ci_failure`, `ci_wait` failure digests) uses `Call.CapTail`: it keeps the END of the text and cuts the beginning (marker first: `[truncated: first N lines omitted]`), because the failure is at the end.
- Diffs/logs have their own caps (sections 8.4 and 10.2) applied first.
6.7 Pagination rules: params `page` (>=1, default 1) and `limit` (default `FORGEJO_DEFAULT_LIMIT`=20, clamped to the server's `max_response_items`, which is read once from `/settings/api`; always send `limit` explicitly because issue search otherwise defaults to the UI page size). Total from `X-Total-Count`; for `ListActionRuns`/`ListActionTasks` from body `total_count`; otherwise unknown. `more` = `page*limit < total` (never trust `Link`: URLs are built from ROOT_URL). Exceptions with **unknown total** even though `X-Total-Count` is present: `GET R/issues/{n}/timeline` (the header is the length of the current page after server-side removal of code comments and inaccessible cross-references) and any list the server post-filters (user/org repo lists, PR reviews with other users' pending reviews removed): for these `more` = the RAW page was full (`len(raw) == limit` before our filtering), pages may be short, and the header uses the unknown-total form. When the filtered page is empty but the raw page was full, the tool requests the next page (at most 3 extra) before answering. Lists the server does not paginate (issue comments, issue labels, branch protections, review comments, pinned, job lists, push mirrors in some builds) are windowed client-side with the same `page/limit` and header. `limit` max accepted by us: 100 for client-windowed lists, server max otherwise. Null items in server arrays (PR list, timeline) are dropped and counted: header shows `(2 hidden)`.
6.8 `fields`/`verbose`: `verbose=true` (bool, only on `get`-type actions and `repo get`, `pr get`, `issue get`, `run get`) adds the second tier of fields listed as "verbose" in 6.9; `fields` exists only on `forgejo_api` (as `pick`). No other tool has `fields`, to keep schemas small. The "show more" knobs are exactly: `include` (which sections to add), `verbose` (second-tier fields), `full` (lift caps) and `max_chars` (size). There is no generic `detail` param (only `forgejo_endpoints` has one).

### 6.9 Projection field lists and line formats

For each object: API JSON paths kept -> compact line (list) / key order (single). Everything else is dropped. "v:" = only with `verbose`.

| Type | Source paths | List line | Single object keys |
|---|---|---|---|
| PR | `number,title,state,draft,merged,mergeable,user.login,base.ref,head.label,head.sha,head.repo.full_name,base.repo.full_name,labels[].name,assignees[].login,requested_reviewers[].login,milestone.title,comments,review_comments,additions,deletions,changed_files,created_at,updated_at,closed_at,merged_at,merged_by.login,merge_commit_sha,due_date,html_url,body,is_locked,allow_maintainer_edit(v),base.sha(v)` | `#42 draft\|open\|merged\|closed \| title \| by alice \| head->base \| conflict(if open&&!mergeable) \| fork:o/r(if head repo != base repo) \| [labels] \| @assignees \| upd T` | `pr #42`, state, title, by, head->base (+sha8), mergeable/conflict, labels, assignees, reviewers, milestone, due, comments c:, diff `+a -d files:n`, created, updated, merged (by, at, commit sha8), url, body |
| Issue | `number,title,state,user.login,assignees[].login,labels[].name,milestone.title,comments,updated_at,created_at,closed_at,due_date,is_locked,pull_request.merged,pull_request.draft,ref,html_url,body,assets[].{id,name,size}` | `#12 open issue\|pr \| title \| by alice \| [labels] \| @assignees \| ms:v1 \| c:3 \| upd T` (search adds `owner/repo#12`) | like PR, plus `ref`, `attachments: id:name(size)` |
| Comment | `id,user.login,body,created_at,updated_at,assets[].name` (from `issue_url`: issue number in feed) | `c1234 alice 2026-11-22 10:15 \| body(300 one-lined)` | multi-line body |
| Timeline event | `id,type,user.login,created_at,body,label.name,old_milestone/milestone.title,assignee.login,removed_assignee,old_title,new_title,old_ref,new_ref,ref_issue.{number,repository.full_name},ref_commit_sha,review_id,dependent_issue.number,tracked_time.time` | `e<id> T alice close` / `label +bug` / `assign @bob` / `title "a"->"b"` / `ref owner/repo#9` / `review #77` / `merge` / `push` / `comment: text` | |
| Review | `id,user.login,team.name,state,body,commit_id,stale,official,dismissed,comments_count,submitted_at` | `r77 alice approve official \| 8a1b2c3d \| stale \| dismissed \| c:2 \| T \| body(300)`. State words are the lowercase `review.event` enum: `APPROVED`->`approve`, `REQUEST_CHANGES`->`request_changes`, `COMMENT`->`comment`, `PENDING`->`pending` (the same tokens the model passes back as `event`); `REQUEST_REVIEW` rows are collapsed into `requested:` | |
| ReviewComment | `id,path,position,original_position,body,user.login,resolver.login,diff_hunk` | `rc9 src/a.go:42 alice (resolved by bob) \| body` (old side: `src/a.go:-17`) | diff_hunk last 3 lines when `verbose` |
| Label | `id,name,color,description,exclusive,is_archived` | `id 5 \| bug \| #d73a4a \| desc(60) \| exclusive \| archived` | |
| Milestone | `id,title,state,open_issues,closed_issues,due_on,description` | `id 3 \| v1.2 \| open \| 4/10 closed \| due 2026-12-01` | |
| Repo | `full_name,description,private,fork,parent.full_name,mirror,template,archived,empty,default_branch,stars_count,forks_count,open_issues_count,open_pr_counter,size,language,updated_at,permissions,topics,has_*` (flags), `html_url,clone_url,ssh_url`(v), merge settings (v) | `owner/name \| private\|public \| fork\|mirror\|archived\|empty flags \| stars:12 \| upd T \| desc(80)` | `repo owner/name` then keys |
| Branch | `name,commit.id,commit.message,commit.timestamp,protected,user_can_push,user_can_merge,required_approvals,enable_status_check,status_check_contexts` | `main \| 8a1b2c3d \| 2026-11-20 \| protected \| subject(80)` | |
| Tag | `name,commit.sha,message,id` | `v1.0 \| 8a1b2c3d \| message first line` | |
| Release | `id,tag_name,name,draft,prerelease,target_commitish,published_at,author.login,body,assets[].{id,name,size,download_count}` | `id 9 \| v1.0 \| name \| draft\|prerelease \| T \| assets:2` | assets listed `asset 4 file.zip 1.2MB dl:7` |
| Commit | `sha,commit.message,commit.author.{name,date},files[].{filename,status},parents[].sha`; `stats.{additions,deletions}` only for `get` (`stat=true`) | `8a1b2c3d 2026-11-22 alice \| subject` (no `+a -d` in lists: the list request sends `stat=false` to avoid a diff per commit) | + body, parents, files, `+a -d` |
| ChangedFile | `filename,previous_filename,status,additions,deletions` | `M src/a.go +4 -1` (`A` `D` `R old->new` `C`) | |
| Run | `id,index_in_repo,workflow_id,title,status,event,prettyref,commit_sha,trigger_user.login,started,stopped,duration(ns),created,need_approval,is_fork_pull_request` | `run 1234 no.56 failure \| ci.yml \| push main 8a1b2c3d \| by alice \| 1m03s \| T` (`approval-needed` flag; `no.N` is the per-repo run number, never `#`) | + title, jobs |
| Job | `id,name,status,attempt,needs,runs_on,task_id` | `job 5678 build failure a1 needs:lint on:docker` | + steps |
| Step | `number,name,status,started,stopped` | `  step 3 Run tests failure 12s` | |
| Runner | `id,name,status,labels,version,ephemeral,description,repo_id,owner_id` | `runner 7 build-1 idle \| repo\|owner\|global \| v6.3.1 \| [docker,ubuntu-latest] \| eph` | |
| Artifact | `id,name,size_in_bytes,run_id,expired,expires_at` | `artifact 3 dist.zip 1.2MB run:1234 expires T (expired)` | |
| Status | `status,context,description,target_url` | `failure ci / build (push) \| Failing after 12s \| url` | |
| Secret/Var | `name,created_at` / `name,data` | `NAME` / `NAME=value(200)` | |
| Hook | `id,type,active,events,branch_filter,config.url,config.content_type` | `id 2 \| forgejo \| active \| push,pull_request \| url` | |
| Key | `id,title,fingerprint,read_only,created_at` | `id 4 \| title \| SHA256:… \| ro` | |
| User | `login,id,full_name,email,is_admin,active,created` | `alice \| Alice A \| admin` | |
| Org | `name,id,full_name,visibility,description` | `org \| visibility \| desc(80)` | |
| Team | `id,name,permission,includes_all_repositories,units_map,description` | `team 3 devs write \| all-repos` | |
| Notification | `id,unread,pinned,updated_at,repository.full_name,subject.{title,type,state,url→n}` | `n77 unread owner/repo#12 pr open \| title \| T` | |
| Package | `type,name,version,created_at,repository.full_name` | `npm foo 1.0.2 T repo:o/r` | |
| WikiPage | `title,sub_url,last_commit.committer.date,content_base64(decoded)` | `title \| sub_url \| upd T` | |
| Collaborator | `login` (+ permission on get) | `alice` | |
| Cron | `name,schedule,next,prev,exec_times` | `name \| schedule \| next T \| runs:n` | |

Implementation: each area owns `project.go` with structs containing only the paths above (JSON decode drops everything else, so raw bodies are never held longer than needed) and `render.go` producing the lines. Golden tests per renderer.

## 7. Tool catalog and shared parameter vocabulary

30 tools. Tool name = first column. Toolset in brackets. Descriptions are the literal `Tool.Description` strings (<= 160 chars); parameter docs live in `jsonschema` tags (<= 12 words) and in `action=help`.

| # | Tool [toolset] | Description (literal) |
|---|---|---|
| 1 | `forgejo_info` [meta] | Server version, API limits, who am I, default repo, enabled toolsets. Call first when unsure. |
| 2 | `forgejo_api` [meta] | Raw Forgejo API call (method, path, query, body) with field pick and paging. Use for anything without a dedicated tool. |
| 3 | `forgejo_endpoints` [meta] | Search the Forgejo API index (method, path, params) to find the right forgejo_api call. |
| 4 | `repo` [repos] | Repositories: get, list, search, create, edit, delete, fork, transfer, migrate, topics, mirrors, star. |
| 5 | `branch` [repos] | Branches and branch protection: list, get, create, rename, delete, protect_*. |
| 6 | `tag` [repos] | Tags and tag protection: list, get, create, delete, protect_*. |
| 7 | `release` [repos] | Releases and their assets: list, get, create, edit, delete, asset_*. |
| 8 | `wiki` [repos] | Wiki pages: list, get, create, edit, delete, history. |
| 9 | `file` [repos] | Repo files: read, ls, tree, write, delete, multi-file commit, apply patch, archive. |
| 10 | `commit` [repos] | Commits: list, get (stat or patch), compare refs, find the PR of a commit. |
| 11 | `pr` [pulls] | Pull requests: list, get (with CI and review summary), create, edit, merge, update branch, files, diff, commits. |
| 12 | `review` [pulls] | PR reviews: list with inline comments, create, submit, dismiss, delete, request/unrequest reviewers. |
| 13 | `issue` [issues] | Issues (and PR metadata by number): list, search, get, create, edit, delete, lock, pin, link, react, subscribe. |
| 14 | `comment` [issues] | Issue/PR comments: list, add, edit, delete, timeline, repo-wide feed. |
| 15 | `label` [issues] | Labels of a repo or org: list, create, edit, delete. |
| 16 | `milestone` [issues] | Milestones: list, create, edit, delete. |
| 17 | `attach` [issues] | Issue/comment attachments: list, upload, download, rename, delete. |
| 18 | `run` [actions] | Actions runs and jobs: list, get, job, log (tail/grep/step), cancel, rerun, delete, dispatch, workflows, artifacts. |
| 19 | `ci_failure` [actions] | One call: why did CI fail. Failed jobs with the tail of the failing step's log, for a run, PR, ref or sha. |
| 20 | `ci_wait` [actions] | Block server-side until a run or PR/ref checks finish or timeout; returns the verdict. |
| 21 | `checks` [actions] | Commit statuses and combined CI state for a ref/PR; set a status. |
| 22 | `actions_config` [actions] | Actions secrets and variables at repo, org or user scope: list, get, set, delete. |
| 23 | `runner` [runners] | Runners at repo/org/user/admin scope: list, get, register, delete, queue, reg_token. |
| 24 | `hook` [settings] | Webhooks at repo/org/user/admin scope: list, get, create, edit, delete, test. |
| 25 | `access` [settings] | Collaborators and deploy keys: list, add, remove, permission. |
| 26 | `user` [users] | Users: me, get, search, followers, following, starred, orgs, keys, tokens. |
| 27 | `org` [users] | Organizations and teams: get, list, create, edit, delete, members, team_*. |
| 28 | `notify` [users] | Notifications: count, list, get, mark read/unread/pinned. |
| 29 | `package` [packages] | Packages: list, get, files, delete, link, unlink. |
| 30 | `admin` [admin] | Site admin: users, orgs, emails, cron, system hooks, unadopted repos, quota. |

### 7.1 Shared parameter vocabulary (same name = same meaning everywhere)

`action` (required enum) | `repo` string `owner/name` (see 3.1; on every repo-scoped tool) | `org` string | `user` string | `n` int (issue/PR number) | `id` int (numeric id of the thing the tool manages: label, milestone, hook, release, runner, artifact, comment) | `name` string (branch, tag, label name, repo name) | `ref` string (branch/tag/sha) | `sha` string | `path` string | `q` string (search text) | `state` string | `page` int>=1 | `limit` int 1..100 | `body` string | `title` string | `labels` string[] (names) | `assignees` string[] | `milestone` string (title or id) | `full` bool | `verbose` bool | `body_max` int | `confirm` string | `scope` string enum (runner/hook/secret level) | `timeout` int seconds | `max_chars` int (6.6) | `include` string[] (sections to add) | `first` int (head lines of a log) | `which` (repo list selector) | `all` bool.
Bool params whose server default is true use `*bool` in Go (nil = leave server/our default). Params that the chosen action does not use are ignored and reported on a last line `note: ignored params: x,y` (lets the model self-correct without an error). The generated schema tells which actions read a param (section 2, property description prefix), so the first call usually needs no `help`.

`action=help` (every multi-action tool; also `help=<action>` on any call): returns the action table of that tool: for each action `name [R|W|D] params(required marked *) - summary`, or, for one action, the full parameter list with types and defaults. This is where per-action parameter docs live so tool descriptions stay one line. Output is generated from `Action.Params/Required/Summary`, never hand-written twice.

## 8. Tools in detail

Layout of each subsection: params (JSON name: type, enum, default), then actions as `action | mode | required / optional | endpoints | output`. Mode R/W/D. All endpoints relative to `/api/v1`. `R` = `/repos/{owner}/{repo}`. "pg" = paginated list per 6.7.

### 8.1 meta (internal/tools/meta)

**forgejo_info** `{verbose?: bool, refresh?: bool}` Read. Calls (cached unless `refresh`): `GET /version`, `GET /settings/api`, `GET /settings/repository`, `GET /user` (anonymous -> `me: anonymous`), repo detection result, enabled toolsets, read-only flag. `verbose=true` additionally: `GET /users/{me}/tokens` (best effort, errors ignored) and match `token_last_eight` against the last 8 chars of our token to print `scopes:`; probes `GET /user/quota` (404 = quota disabled) and, when a repo resolves, `GET R/actions/runs?limit=1` (404/403 = Actions disabled or no access).
Output:
```
forgejo 17.0.2 (limits: default 30, max 50) https://forgejo.example.com
me: alice (id 4, admin) | token scopes: all
repo: owner/name (git:origin) branch: fix/login
toolsets: repos,pulls,issues,actions,runners,settings,users,packages | read-only: no | 30 tools
disabled on server: stars, migrations
warn: server major 14 < 15, some actions unsupported
```
**forgejo_api** and **forgejo_endpoints**: contract in section 9.

### 8.2 repos (internal/tools/repos: repo, branch, tag, wiki; the `release` tool below lives in internal/tools/content)

#### `repo`
Params: `action`, `repo`, `which` enum `mine|user|org|starred|watched` (list, default mine; not `scope`, which means runner/hook/secret level elsewhere), `owner` (user/org name for list/create), `q`, `topic` bool, `mode` enum `source|fork|mirror|collaborative`, `sort`, `order` enum `asc|desc`, `archived` *bool, `private` *bool (search: filter, sent as `is_private`; create: visibility), `name`, `description`, `auto_init` *bool (default true), `readme`, `license`, `gitignores`, `default_branch`, `template` bool, `from_template` string, `set` object (edit fields), `new_owner`, `team_ids` int[], `clone_addr`, `service`, `mirror` bool, `auth_username`, `auth_token`, `include` string[] (`wiki,issues,pull_requests,releases,labels,milestones,lfs`), `interval` (Go duration such as `8h`, `30m`; `0` = on-commit only for push mirrors), `branch` (push-mirror `branch_filter`), `remote_address`, `remote_name`, `use_ssh` bool, `sync_on_commit` *bool, `topics` string[], `confirm`, `verbose`, `page`, `limit`.

| action | mode | required / optional | endpoints | output |
|---|---|---|---|---|
| `get` | R | repo; verbose | `GET R` | repo object (6.9); verbose adds clone/ssh urls, merge settings (`allow_*`, default_merge_style), has_* flags, mirror info, parent |
| `list` | R | which=mine, owner (for user/org), sort; pg | `GET /user/repos` (mine; `order_by` from sort), `GET /users/{owner}/repos`, `GET /orgs/{owner}/repos`, `GET /user/starred`, `GET /user/subscriptions` | repo lines; user/org lists may return short pages (server post-filters), header notes `(page may be short)` |
| `search` | R | q; topic, mode, archived, private, sort (`alpha,created,updated,size,stars,forks,id`), order; pg | `GET /repos/search` (unwrap `{ok,data}`; params sent only when set). Param mapping: tool `private=true\|false` -> `is_private=<bool>` (restrict to private/public repos); the server's own `private` param means "include private repos" (default true when signed in) and is NEVER sent by this tool; `topic` -> `topic`, `mode` -> `mode`, `archived` -> `archived`; server-side `template`, `exclusive`, `uid`, `starredBy` are reachable through `forgejo_api` | repo lines |
| `create` | W | name; owner (org), description, private, auto_init (default true so the repo is not empty), readme, license, gitignores, default_branch, template, from_template | `POST /user/repos` or `POST /orgs/{owner}/repos`; with `from_template` (`owner/name`): `POST /repos/{t_owner}/{t_repo}/generate` `{owner: owner or me, name, description, private, git_content:true, topics:true, labels:true}` | `ok: created owner/name (private) <html_url>` |
| `edit` | W | repo, set{...} | `PATCH R` with `set` filtered to the EditRepoOption keys (unknown key -> error listing valid keys); invalidates repo cache | `ok: edited owner/name: description,private`; adds `note: repo edits are not atomic` when several groups were set |
| `delete` | D | repo, confirm=`owner/name` | `DELETE R` | `ok: deleted owner/name (irreversible)` |
| `fork` | W | repo; owner (org to fork into), name | `POST R/forks` `{organization,name}` (202; the fork is completely created before the server answers, so there is no polling); then one `GET /repos/{fork}` to report | `ok: forked to o2/n` (`(empty source repo)` appended when the new repo is `empty`) |
| `forks` | R | repo; pg | `GET R/forks` | repo lines |
| `transfer` | W | repo, new_owner; team_ids | `POST R/transfer` (201 done / 202 pending) | `ok: transferred` or `ok: transfer pending; recipient runs repo.transfer_accept` |
| `transfer_accept` / `transfer_reject` | W | repo | `POST R/transfer/accept` / `POST R/transfer/reject` | ok line |
| `migrate` | W | clone_addr, name; owner, service, mirror, private, auth_username, auth_token, include, interval | `POST /repos/migrate` with the SERVER's key names: `{clone_addr, repo_name (tool: name), repo_owner (tool: owner), service, mirror, private, description, auth_username, auth_token, mirror_interval (tool: interval, Go duration), wiki, issues, pull_requests, releases, labels, milestones, lfs}` (`include` maps to the booleans). Sent from a detached context (10 min); progress notification every 10 s; if the server has not answered after `FORGEJO_WAIT_MAX` s the tool returns `started; migration still running on the server; check with repo get owner/name` (clients typically abort a call at 60 s). A clone_addr containing credentials is rejected locally | `ok: migrated owner/name`; credentials never echoed |
| `topics` | R | repo | `GET R/topics` | `topics: a,b,c` |
| `topics_set` | W | repo, topics[] (replaces all) | `PUT R/topics` | ok; 422 `invalidTopics` listed |
| `languages` | R | repo | `GET R/languages` | `Go 81.2% (123KB) \| JS 18.8%` |
| `mirror_sync` | W | repo | `POST R/mirror-sync` | `ok: pull-mirror sync queued` |
| `pushmirror_list` | R | repo; pg | `GET R/push_mirrors` | `remote_mirror_ab12 -> url every 8h0m0s on-commit last T err:...` |
| `pushmirror_add` | W | repo, remote_address; auth_username, auth_token, interval, sync_on_commit, use_ssh, branch | `POST R/push_mirrors` (returns 200) body `{remote_address, remote_username (tool: auth_username), remote_password (tool: auth_token), interval, sync_on_commit, use_ssh, branch_filter (tool: branch)}`. `interval` is ALWAYS sent (the server runs `time.ParseDuration` on it and answers 400 for an empty string): default `8h0m0s`; `0` = push on commit only (then `sync_on_commit` defaults to true); any other Go duration is normalised with `Duration.String()`; the server also rejects non-zero values below its minimum (default 10m, `[v]`) with 400 and the tool passes that message. `use_ssh=true` together with `auth_username`/`auth_token` is rejected locally (`error: invalid: use_ssh cannot be combined with credentials`; the server answers 400). Output includes `public_key` when `use_ssh` | ok line |
| `pushmirror_delete` | W | repo, remote_name | `DELETE R/push_mirrors/{remote_name}` | ok |
| `pushmirror_sync` | W | repo | `POST R/push_mirrors-sync` | ok |
| `fork_status` | R | repo; branch | `GET R/sync_fork` or `GET R/sync_fork/{branch}` | `behind 3 commits, sync allowed` |
| `fork_sync` | W | repo; branch | `POST R/sync_fork[/{branch}]` | ok |
| `star` / `unstar` | W | repo | `PUT` / `DELETE /user/starred/{owner}/{repo}` | ok |

#### `branch`
Params: `action`, `repo`, `name`, `from` (any ref/sha), `new_name`, `rule` (protection rule name or glob such as `release/*`; default = `name`; sent as ONE path segment with `Seg()`, so `/` becomes `%2F`: the route is `/branch_protections/{name}`, not a wildcard), `set` object (protection fields: `enable_push, enable_push_whitelist, push_whitelist_usernames, push_whitelist_teams, push_whitelist_deploy_keys, enable_merge_whitelist, merge_whitelist_usernames, merge_whitelist_teams, enable_status_check, status_check_contexts, required_approvals, enable_approvals_whitelist, approvals_whitelist_username, approvals_whitelist_teams, block_on_rejected_reviews, block_on_official_review_requests, block_on_outdated_branch, dismiss_stale_approvals, ignore_stale_approvals, require_signed_commits, protected_file_patterns, unprotected_file_patterns, apply_to_admins`), `q` (client-side substring filter), `page`, `limit`.

| action | mode | params | endpoints | output |
|---|---|---|---|---|
| `list` | R | q; pg | `GET R/branches` (no server filter: with `q` scan up to 5 pages and filter client-side, header says `(filtered client-side, scanned N)`) | branch lines |
| `get` | R | name | `GET R/branches/{name}` | branch + protection summary (`required_approvals`, `status_check_contexts`) |
| `create` | W | name; from | `POST R/branches` `{new_branch_name, old_ref_name}` | `ok: created name from ref@8a1b2c3d` |
| `rename` | W | name, new_name | `PATCH R/branches/{name}` `{name: new_name}` | ok |
| `delete` | W | name | `DELETE R/branches/{name}` | ok; 403 explains default/protected branch |
| `protect_list` | R | - | `GET R/branch_protections` (client-windowed) | `rule main \| approvals:1 \| checks:ci/build* \| push-whitelist:2` (non-default flags only) |
| `protect_get` | R | rule | `GET R/branch_protections/{rule}` | key: value lines, non-default only |
| `protect_set` | W | rule, set | `GET R/branch_protections/{rule}`: 404 -> `POST` with `rule_name` + set; 200 -> `PATCH` with set only. When `set` contains `enable_push_whitelist` or `push_whitelist_deploy_keys` but not `enable_push`, the tool adds `enable_push:true` to the body, because the server applies those two fields only inside an `enable_push=true` update and would silently ignore them (output adds `note: enable_push:true added; whitelist options need it`; an explicit `enable_push:false` together with them is rejected locally) | `ok: protection main created\|updated: required_approvals,enable_status_check`; note that `enable_push:false` also clears the push whitelist |
| `protect_delete` | W | rule | `DELETE R/branch_protections/{rule}` | ok |

#### `tag`
Params: `action`, `repo`, `name`, `target`, `message`, `rule_id` int, `set` (`name_pattern, whitelist_usernames, whitelist_teams`), `verbose`, `page`, `limit`.
Actions: `list` R (`GET R/tags`), `get` R (`GET R/tags/{name}`; with `verbose`, annotated detail via `GET R/git/tags/{id}` only when `tag.id != tag.commit.sha`; for a lightweight tag (equal ids) it prints `lightweight tag` and makes no second call, since the endpoint answers 400 for non-annotated objects), `create` W (`POST R/tags` `{tag_name, target, message}`), `delete` W (`DELETE R/tags/{name}`; 409 hint: delete the release first with `release.delete`), `protect_list` R (`GET R/tag_protections`), `protect_get` R (`GET R/tag_protections/{rule_id}`), `protect_set` W (no `rule_id` -> `POST`, else `PATCH`; both whitelists empty rejected locally with the server's message), `protect_delete` W (`DELETE R/tag_protections/{rule_id}`).

#### `release`
Params: `action`, `repo`, `id`, `tag`, `title`, `body`, `target`, `draft` *bool, `prerelease` *bool, `q`, `asset_id`, `file` (local path to upload), `save_to`, `url` (external asset), `page`, `limit`. The release `tag` is always sent as ONE path segment with `Seg()` (`/releases/tags/{tag}` is a single-segment route, unlike the wildcard `/tags/*`): a tag `v1/rc` becomes `v1%2Frc`.
| action | mode | endpoints | notes |
|---|---|---|---|
| `list` | R | `GET R/releases` (`draft`, `pre-release` (literal hyphen), `q`) | `id 9 \| v1.0 \| title \| prerelease \| T \| assets:2` |
| `get` | R | `id`, or `tag`, or `tag=latest`: `GET R/releases/{id}`, `GET R/releases/tags/{tag}`, `GET R/releases/latest` | body cap 2000 |
| `create` | W | `POST R/releases` `{tag_name,target_commitish,name,body,draft,prerelease}` | 409 -> `release exists for tag; use release.edit` |
| `edit` | W | tag -> id via `GET R/releases/tags/{tag}`; `PATCH R/releases/{id}` | server ignores empty strings (cannot clear body/title): empty values are rejected locally with that message |
| `delete` | W | `DELETE R/releases/{id}` or `DELETE R/releases/tags/{tag}` | `ok: deleted release (git tag may remain)` |
| `asset_list` | R | `GET R/releases/{id}/assets` | `asset 4 file.zip 1.2MB dl:7` |
| `asset_upload` | W | `file`: `POST R/releases/{id}/assets?name=<basename>` with the raw file as body streamed from `file` (`Content-Type: application/octet-stream`, non-multipart: the server reads the body as the attachment). `url` (external asset): `POST R/releases/{id}/assets?name=<name>&external_url=<url>` sent as `Content-Type: multipart/form-data` with an EMPTY multipart body (no `attachment` part); a non-multipart request without a file would still carry a non-nil server-side body and always fail with 400 `'attachment' and 'external_url' are mutually exclusive` | bytes never enter the model context; timeout 10 min (not detached: README documents the client tool timeout) |
| `asset_edit` | W | `PATCH R/releases/{id}/assets/{asset_id}` `{name}` | ok |
| `asset_delete` | W | `DELETE R/releases/{id}/assets/{asset_id}` | ok |
| `asset_download` | R | `asset_id` (and `id`/`tag`) -> asset from `GET R/releases/{id}/assets`; an external asset is not downloaded (`external asset: <url>`, rule 5.7); an internal one: only the URL path of `browser_download_url` is used, rebuilt on `FORGEJO_URL` (`API:"root"`), fetched with `Client.Download` (cross-host storage redirects followed without Authorization, 5.2) and written to `save_to` (rules 10.5) | `ok: saved path (1.2MB)` |

#### `wiki`
Params: `action`, `repo`, `page_name` (the `sub_url` from `list`), `title`, `content` (plain text; the tool base64-encodes), `message`, `body_max`, `page`, `limit`.
Actions: `list` R (`GET R/wiki/pages`), `get` R (`GET R/wiki/page/{page_name}`; content decoded, capped by `body_max`), `create` W (`POST R/wiki/new` `{title,content_base64,message}`), `edit` W (`PATCH R/wiki/page/{page_name}`; a different `title` renames), `delete` W (`DELETE R/wiki/page/{page_name}`), `history` R (`GET R/wiki/revisions/{page_name}`).

### 8.3 content (internal/tools/content: release.go, file.go, commit.go)

#### `file`
Params: `action`, `repo`, `path`, `ref`, `branch`, `new_branch`, `message`, `content`, `content_base64`, `sha` (blob sha, optional), `from_path`, `ops` array of `{op: create|update|delete|rename, path, content, content_base64, from_path}`, `diff` (unified diff text), `glob`, `depth` int, `recursive` bool (default true for tree), `grep`, `start_line`, `end_line`, `max_chars` (default 12000, ceiling 100000; 6.6), `numbers` bool, `format` enum `zip|tar.gz|bundle`, `save_to`, `page`, `limit`.

| action | mode | required / optional | endpoints | output |
|---|---|---|---|---|
| `read` | R | path; ref, start_line, end_line, grep, max_chars, numbers | `GET R/raw/{path}?ref=`; in-process ETag cache of the last 32 reads (`If-None-Match`, 304 reuses). Binary (NUL in first 8000 bytes) returns no content. We keep the first `max_chars` characters of the body | `file src/a.go ref main 4.1KB lines 1-80/212` then the text (`N: ` line prefixes only when `numbers`); `grep` returns matching lines with numbers, no context; marker `[truncated: 12000 of 48211 chars; pass start_line/end_line, grep or max_chars]`. A directory path behaves like `ls` |
| `ls` | R | path (default root); ref | `GET R/contents/{path}?ref=` (array = directory) | `d src/`, `f main.go 1.2KB`, `l link -> target`, `s submodule` one per line, dirs first; a file path returns `error: path is a file; use action=read` |
| `tree` | R | path (prefix), glob, depth, recursive; ref; pg | `GET R/git/trees/{ref or default branch}?recursive=true&per_page=<max>&page=` (follow `truncated` up to 5 pages), filter client-side by prefix/glob/depth | one path per line (dirs end with `/`), header `# paths 120/3400` |
| `write` | W | path, message; content or content_base64; branch, new_branch, sha, from_path | if `sha` empty: `GET R/contents/{path}?ref=<branch>`: 200 -> update with that sha, 404 -> create. Create `POST R/contents/{path}`, update `PUT R/contents/{path}` (`from_path` renames). Empty repo: POST works | `ok: created\|updated src/a.go on main @8a1b2c3d (blob 1b2c3d4e)`; 409 -> `file changed since read; re-read and retry`; protected branch 403 -> hint `use new_branch, then pr create` |
| `delete` | W | path, message; branch, new_branch, sha | resolve sha as above; `DELETE R/contents/{path}` with a JSON body; a sha/commit mismatch answers **400** here (not 409), mapped to the hint `file changed since read; re-read and retry` | ok line |
| `commit` | W | message, ops; branch, new_branch | resolve `sha` for update/delete/rename ops by parallel `GET R/contents/{path}` (<= 8 at once); then `POST R/contents` `{files:[{operation,path,content,sha,from_path}], message, branch, new_branch}` (one commit; rename = `update` + `from_path`) | `ok: committed 3 files on main @8a1b2c3d` |
| `patch` | W | diff, message; branch (default: repo default branch, always sent), new_branch | `POST R/diffpatch` `{content: diff, sha: "x", message, branch, new_branch}` (`sha` is a binding dummy); a patch that does not apply or a changed file answers **422**, mapped to the hint `file changed since read; re-read the file and rebuild the diff` | `ok: applied patch @8a1b2c3d` |
| `archive` | R | ref, format=zip, save_to | `GET R/archive/{ref}.{format}` (blocks until built; streamed to disk) | `ok: saved path (3.1MB)` |

#### `commit`
Params: `action`, `repo`, `ref` (branch/tag/sha), `sha`, `path`, `paths` string[] (globs for patch filter), `not`, `base`, `head`, `include` string[] enum `stat|patch` (get only; default none), `max_chars` (patch, default 14000, ceiling 100000; 6.6), `verbose`, `page`, `limit`.
| action | mode | endpoints | output |
|---|---|---|---|
| `list` | R | `GET R/commits?sha=<ref>&path=&not=&stat=false&files=false&verification=false&page&limit` (limit clamped to <= 50, the server's `CommitsRangeSize`; `X-HasMore` used for more); empty repo 409 -> `repo is empty` | commit lines |
| `get` | R | `GET R/git/commits/{sha}?stat=true&files=true&verification=false`; `include=[patch]` also `GET R/git/commits/{sha}.diff` (split/filter/cap per the diff rules in 8.4) | commit header + message + `files:` lines (`M a.go`) + totals `+a -d` (the server provides no per-file counts here); `include` empty keeps the header and `files:` only |
| `compare` | R | `GET R/compare/{base}...{head}?verification=false&files=true` (`head` may be `owner:branch`) | `total_commits: N` then up to `limit` commit lines (default 20), then unique files with merged status; note `files aggregated per commit, not a net diff` |
| `pulls_of` | R | `GET R/commits/{sha}/pull` (merge commit -> PR) | PR line; 404 -> `no merged PR has this merge commit` |

### 8.4 pulls (internal/tools/pulls)

Shared helper `ci.CombinedStatus(ctx, client, owner, repo, sha)` (package `internal/tools/shared/ci`, owned by WP6b) returns `{State, Total, OK int, Failing []Check, Pending []string}`; used by `pr.get`, `checks.get`, `ci_wait`. It requests `GET R/commits/{ref}/status?limit=<server max>`: the server computes `state` and `total_count` over the returned page only, so when `len(statuses) == limit` it fetches further pages (at most 5) and recomputes the state client-side as the worst one (`failure|error` > `pending` > `warning` > `success`, latest status per context wins).

#### `pr`
Params: `action`, `repo`, `n`, `state` enum `open|closed|merged|all` (list, default open), `author`, `base`, `head`, `labels` string[] (names), `milestone`, `sort` enum `oldest|recentupdate|recentclose|leastupdate|mostcomment|leastcomment|priority`, `title`, `body`, `draft` *bool, `assignees`, `reviewers`, `team_reviewers`, `due`, `unset_due` bool, `allow_maintainer_edit` *bool, `method` enum `merge|rebase|rebase-merge|squash|fast-forward-only|manually-merged`, `merge_title`, `merge_message`, `when_green` bool, `delete_branch` *bool, `head_sha`, `force` bool, `merge_commit_id`, `style` enum `merge|rebase` (update), `include` string[] enum `body|files|commits|ci|reviews|comments`, `body_max`, `paths` string[], `max_chars` (diff, default 14000, ceiling 100000; 6.6), `whitespace` enum `ignore-all|ignore-change|ignore-eol|show-all`, `page`, `limit`, `full`, `verbose`.

| action | mode | req / opt | endpoints | output |
|---|---|---|---|---|
| `list` | R | state=open, author, base, head, labels, milestone, sort; pg | `GET R/pulls?state=&sort=&poster=&base=&head=&labels=<ids>&milestone=<id>`; `state=merged` = server `closed` + client filter `merged`; names -> ids via cache; nulls dropped | PR lines; header `# prs 3/3` |
| `get` | R | n, or head (+base); include; body_max | `GET R/pulls/{n}`; with neither `n` nor `head`: the PR of the target rule 3.1 step 4 (first output line `target: ...`). By branch: with `base` known `GET R/pulls/{base}/{head}` (it parses `owner:branch` and `owner/repo:branch`, so fork PRs resolve); without `base` `GET R/pulls?state=all&head=<branch>&limit=5` first hit (that filter matches plain branch names only, never `owner:branch`). includes: `ci` -> `ci.CombinedStatus(head sha)`; `reviews` -> `shared.AllReviews` (`GET R/pulls/{n}/reviews` with `limit=max`, all pages until an empty page, because pending reviews of other users are dropped after counting and pages can be short), then the latest review per reviewer; `files` -> `GET R/pulls/{n}/files`; `commits` -> `GET R/pulls/{n}/commits?verification=false&files=false`; `comments` -> `GET R/issues/{n}/comments` (last 5). Default `include`: open PR `body,ci,reviews`; closed `body`. Required approvals from `GET R/branches/{base}` | PR object + `ci:` + `reviews:` + `blocked_by:` or `ready: yes` (example below) |
| `create` | W | head (default: current git branch if repo matches), title; base (default branch), body, draft, labels, assignees, reviewers, team_reviewers, milestone, due | `POST R/pulls` `{head,base,title,body,assignees,milestone,labels(ids),due_date}`; `draft` -> title prefixed `WIP: `; reviewers via follow-up `POST R/pulls/{n}/requested_reviewers`; 409 -> look up the existing PR for head/base and report `error: PR already exists: #41` | `ok: created pr #43 fix/login->main <html_url>` |
| `edit` | W | n; title, body, base, state (`open\|closed`), draft, labels (replace), assignees (replace), milestone (`none` clears), due, unset_due, allow_maintainer_edit | `GET R/pulls/{n}` first when `draft` is set (toggle the WIP prefix on the current title); `PATCH R/pulls/{n}` (201 = success); `unset_due` sends only `unset_due_date:true`; setting a milestone sends its id in the pulls PATCH, but clearing it (`milestone=none`) goes through `PATCH R/issues/{n}` `{"milestone":0}` because the pulls PATCH ignores 0 while the issue route accepts it for PR numbers | `ok: edited pr #43: title,labels` |
| `merge` | W | n; method, merge_title, merge_message, when_green, delete_branch, head_sha, force, merge_commit_id | flow in 10.3: `POST R/pulls/{n}/merge` body `{Do, MergeTitleField, MergeMessageField, MergeCommitID, head_commit_id, force_merge, merge_when_checks_succeed, delete_branch_after_merge}` (exact key casing); on 201 the tool records `Memo.Set("automerge:<owner/repo>#<n>")` so a following `ci_wait pr=N` waits for the actual merge | `ok: merged pr #42 as 8a1b2c3d (squash)` / `ok: auto-merge scheduled for pr #42 (merges when checks succeed)` / `error: ...` with the quirk text |
| `automerge_cancel` | W | n | `DELETE R/pulls/{n}/merge` | ok; 404 -> `no auto-merge was scheduled` |
| `update` | W | n; style=merge | `POST R/pulls/{n}/update?style=` | `ok: branch updated` / `ok: already up to date` / conflict message |
| `files` | R | n; whitespace; pg (limit default 50) | `GET R/pulls/{n}/files` | `M src/a.go +4 -1` lines, header with totals `+120 -33 in 7 files` |
| `diff` | R | n; paths, max_chars, whitespace | `GET R/pulls/{n}.diff` (text/plain, no server cap): read up to 4 MiB, split, filter, cap | diff rules below |
| `commits` | R | n; pg | `GET R/pulls/{n}/commits?verification=false&files=false` | commit lines |

Diff output rules (shared by `pr.diff` and `commit.get include=[patch]`; implemented once in `internal/format/diff.go`, owned by WP3): split on `^diff --git a/(.+) b/(.+)$`; drop `index ...` lines; for each selected file (all, or those matching any glob in `paths`, using `path.Match` on the full path and on the basename) print `=== <A|M|D|R> <path> (+a -d) ===` then its hunks; stop adding files when the running size would exceed `max_chars`; a single file larger than `max_chars/2` is cut at a line boundary with `[truncated: file shown N of M chars; pass paths=[...] and a larger max_chars]`. Trailer when files were omitted: `[truncated: 3 of 12 files shown, 17.2KB omitted; omitted: b.go, c.go, ...; pass paths=[...] or raise max_chars]` (names capped at 15). Binary files print `=== M logo.png (binary) ===` only. The per-file headers count against `max_chars`; the omitted-files trailer is added OUTSIDE that budget (6.6) so it always survives `Call.Cap`.

Example (`pr get n=42`):
```
pr #42 open | Fix login race
by alice | fix/login->main (8a1b2c3d) | [bug,ui] | @bob | ms:v1.2
created 2026-11-21 09:00 | updated 2026-11-22 10:15 | +12 -3 files:2 | c:3
url: https://forgejo.example.com/o/r/pulls/42
mergeable: yes
ci: failure 3/4 | failing: ci / build (push) "Failing after 12s"
reviews: alice approve | bob request_changes | pending: carol
blocked_by: ci:failure, request_changes:bob, approvals:1/2
body:
  Fixes the race in session refresh. ...
```
Example (`pr list`):
```
# prs 2/2 page 1/1
#42 open | Fix login race | by alice | fix/login->main | [bug,ui] | @bob | upd 2026-11-22 10:15
#40 draft | WIP: new cache | by bob | cache->main | conflict | upd 2026-11-20 08:01
```

#### `review`
Params: `action`, `repo`, `n`, `review_id` int, `id` int (comment id), `event` enum `approve|request_changes|comment|pending`, `body`, `comments` array of `{path, line, side: new|old, body, extra_lines}`, `commit_id`, `message` (dismiss), `reviewers` string[], `team_reviewers` string[], `only_unresolved` bool, `with_comments` bool (default true), `page`, `limit`.
| action | mode | endpoints | notes |
|---|---|---|---|
| `list` | R | `shared.AllReviews` (`GET R/pulls/{n}/reviews` with `limit=max`, all pages until an empty page; the same loop `pr.get` uses); when `with_comments`, `GET R/pulls/{n}/reviews/{id}/comments` for each review with `comments_count>0` (parallel <= 6) | REQUEST_REVIEW rows collapse into `requested: carol,team:devs`; other users' pending reviews hidden; comment lines indented under their review; `only_unresolved` keeps `resolver==null`; state words are the lowercase event enum (6.9) |
| `create` | W | `POST R/pulls/{n}/reviews` `{event: APPROVED\|REQUEST_CHANGES\|COMMENT\|PENDING, body, commit_id, comments:[{path,body,new_position\|old_position,extra_lines_count}]}`; local pre-checks: approve/request_changes on own PR (server 422), request_changes without body | `ok: review r77 APPROVED` |
| `submit` | W | `POST R/pulls/{n}/reviews/{review_id}` `{event,body}` | ok |
| `dismiss` / `undismiss` | W | `POST R/pulls/{n}/reviews/{review_id}/dismissals` `{message}` / `.../undismissals` | needs repo admin |
| `delete` | W | `DELETE R/pulls/{n}/reviews/{review_id}` | ok |
| `request` | W | `POST R/pulls/{n}/requested_reviewers` `{reviewers,team_reviewers}` | `ok: requested alice,team:devs` |
| `unrequest` | W | `DELETE R/pulls/{n}/requested_reviewers` same body | ok |
| `comment_delete` | W | `DELETE R/pulls/{n}/reviews/{review_id}/comments/{id}` | ok |

### 8.5 issues (internal/tools/issues)

#### `issue`
Params: `action`, `repo`, `n`, `state` enum `open|closed|all`, `type` enum `issues|pulls|all` (list default issues; search default all), `q`, `labels` (names), `add_labels`, `remove_labels`, `set_labels`, `milestone` (title or id; `none` clears on edit), `assignees` (replace on edit), `author`, `assigned_to`, `mentioned`, `since`, `before`, `sort` enum `latest|oldest|recentupdate|leastupdate|mostcomment|leastcomment|nearduedate|farduedate`, `title`, `body`, `due` (YYYY-MM-DD), `unset_due`, `ref`, `closed` bool, `reason` (lock; default `resolved`), `relation` enum `blocked_by|blocks`, `other` (`owner/repo#N` or `#N`), `content` (reaction), `comment_id`, `remove` bool, `flags` string[] enum `assigned|created|mentioned|review_requested|reviewed` (search), `owner` (search), `body_max`, `include` string[] enum `comments|timeline`, `confirm`, `page`, `limit`, `full`, `verbose`.
| action | mode | endpoints | notes |
|---|---|---|---|
| `list` | R | `GET R/issues?state=&type=&q=&labels=<names csv>&milestones=<name>&created_by=&assigned_by=&mentioned_by=&since=&before=&sort=&page=&limit=` | unknown label/milestone names validated locally first (the server silently widens the result); lines per 6.9 |
| `search` | R | `GET /repos/issues/search?q=&state=&type=&labels=&milestones=&owner=&assigned=&created=&mentioned=&review_requested=&reviewed=&since=&sort=&page=&limit=` (limit always sent) | lines with `owner/repo#N`; header notes labels are any-of here |
| `get` | R | `GET R/issues/{n}`; `include=comments` -> `GET R/issues/{n}/comments` (last 10, 300 chars each); `timeline` -> `GET R/issues/{n}/timeline` | PR numbers work and print kind `pr` with merged/draft |
| `create` | W | `POST R/issues` (labels/milestone resolved to ids). After create, if requested labels/assignees/milestone are missing in the response: `note: server ignored labels/assignees (no write access)` | `ok: created issue #12 <html_url>` |
| `edit` | W | `PATCH R/issues/{n}` (201 = ok); labels: add `POST R/issues/{n}/labels` (names), set `PUT R/issues/{n}/labels`, remove `DELETE R/issues/{n}/labels/{name}` each; names validated first | `ok: edited #12: title,labels+bug` |
| `delete` | D | `DELETE R/issues/{n}` (repo admin); confirm=`owner/name#n` | ok |
| `lock` / `unlock` | W | `PUT R/issues/{n}/lock` `{"reason": <reason or "resolved">}` (the server binds `reason` as required and non-empty; any free text is accepted; sending `lock_reason` fails with 422 `[Reason]: Required`), `DELETE R/issues/{n}/lock`; 409 -> `ok: already locked` / `ok: already unlocked` (lock answers 409 with an empty body when already locked) | ok |
| `pin` / `unpin` | W | `POST` / `DELETE R/issues/{n}/pin` (moving a pin: `forgejo_api PATCH R/issues/{n}/pin/{pos}`) | ok |
| `link` / `unlink` | W | `POST` / `DELETE R/issues/{n}/dependencies` (relation=blocked_by: {n} depends on other) or `.../blocks` (relation=blocks); body `{index, owner, repo}`. For the `#N` form owner/repo are ALWAYS taken from the canonical repo meta `full_name` (never from user input: the server compares them case-sensitively with the current repo, treats any difference as cross-repo and answers 400 when cross-repo dependencies are disabled, and 404 for empty values). `Hint500`: `dependency already exists or would be circular` | `ok: #12 blocked_by owner/repo#9` |
| `links` | R | `GET R/issues/{n}/dependencies` + `GET R/issues/{n}/blocks` | `blocked_by: owner/repo#9 open "title"` / `blocks: #14 closed "title"` |
| `react` | W | `POST`/`DELETE R/issues/{n}/reactions` or `R/issues/comments/{comment_id}/reactions` `{content}` (DELETE when `remove`) | ok |
| `subscribe` / `unsubscribe` | W | `PUT` / `DELETE R/issues/{n}/subscriptions/{me}` (other users: `forgejo_api`) | ok |
| `templates` | R | `GET R/issue_templates` | `name \| title \| [labels] \| fields: id(type,required)` |
| `assignable` | R | `GET R/assignees` | `alice,bob,...` |

#### `comment`
Params: `action`, `repo`, `n`, `id` (comment id), `body`, `last` int, `since`, `from_id`, `types` string[] (timeline filter), `page`, `limit`, `body_max`, `full`.
| action | mode | endpoints | notes |
|---|---|---|---|
| `list` | R | `GET R/issues/{n}/comments?since=` (server returns all; client window: `last` default 10, or page/limit, or `from_id`) | header `# comments 10/37 (newest 10; pass last=N or page=)` |
| `add` | W | `POST R/issues/{n}/comments` `{body}` (works on PRs) | `ok: comment c1234 on #12` |
| `edit` | W | `PATCH R/issues/comments/{id}` `{body}`; a 204 means non-editable type -> `error: not an editable comment` | ok |
| `delete` | W | `GET R/issues/comments/{id}` first (existence; 404 -> `error: 404 comment not found`), then `DELETE R/issues/comments/{id}`. The server answers 204 WITHOUT deleting when the comment is not of type Comment (review or code comments): when the GET already says so, or the comment still exists afterwards, the tool prints `error: not a plain comment; use review.comment_delete` instead of `ok` | ok |
| `timeline` | R | `GET R/issues/{n}/timeline?page&limit` (null items and `code` items dropped; `types` filters client-side). The total is UNKNOWN (6.7: `X-Total-Count` counts the filtered page only): header `# timeline 20 page 1 next: page=2`, `next:` whenever the raw page was full, pages may be short | event lines (6.9) |
| `feed` | R | `GET R/issues/comments?since=&page&limit` (repo-wide) | `c1234 #12 alice T \| body` |

#### `label` and `milestone`
`label` params: `action`, `repo`, `org`, `id`, `name`, `new_name`, `color`, `description`, `exclusive` *bool, `archived` *bool, `sort`, `page`, `limit`. Actions: `list` R (`GET R/labels`, or with `org` `GET /orgs/{org}/labels`; repo labels exclude org labels), `create` W (`POST R/labels` or `POST /orgs/{org}/labels`), `edit` W (resolve by name via cache -> id; fetch current and always resend `is_archived`, since omission resets it; `PATCH`), `delete` W. Cache invalidated on every mutation.
`milestone` params: `action`, `repo`, `id`, `title`, `new_title`, `description`, `due`, `state` enum `open|closed|all` (list default `open`, always sent because omission returns all), `page`, `limit`. Actions: `list` R (`GET R/milestones?state=`), `create` W (`POST R/milestones`), `edit` W (`PATCH R/milestones/{id or title}`; `state` closes/reopens; due date cannot be cleared, documented), `delete` W (`DELETE R/milestones/{id}`).

#### `attach`
Params: `action`, `repo`, `n`, `comment_id`, `id` (attachment id), `file` (local path), `name`, `save_to`.
Actions: `list` R (`GET R/issues/{n}/assets` or `GET R/issues/comments/{comment_id}/assets`), `upload` W (`POST .../assets?name=` multipart field `attachment` streamed from `file`), `download` R (choose the asset from the list; an external asset is not downloaded; an internal one uses only the URL path of `browser_download_url`, rebuilt on `FORGEJO_URL` with `API:"root"` and `Client.Download`, rule 5.7, written to `save_to` under 10.5), `rename` W (`PATCH .../assets/{id}` `{name}`), `delete` W (`DELETE .../assets/{id}`). Output `ok:` lines with id/name/size.

### 8.6 actions (internal/tools/actions: `run`; internal/tools/ci: `ci_failure`, `ci_wait`, `checks`, `actions_config`; shared plumbing in internal/tools/shared/ci)

IDs: `run` = global run id (`ActionRun.id`, used in all API paths); `number` = per-repo run number shown in the UI (`index_in_repo`; rendered `no.56` in output so it is not confused with `#12` PRs), resolved with `GET R/actions/runs?run_number=N` -> `.workflow_runs[0].id`. Job id is global. Status words: `unknown|waiting|running|success|failure|cancelled|skipped|blocked`; done = `success|failure|cancelled|skipped`.
Ref normalisation for `ref`/`pr` filters (`internal/tools/actions/refs.go`): `main` -> `refs/heads/main`; `pr:12` or param `pr=12` -> `refs/pull/12/head`; `#12` is NOT accepted here (an `error: invalid: use pr=12`), because `#` means issue/PR number everywhere else and run numbers are written `no.56`; `refs/...` passes through; a bare name that returns no runs as a branch is retried once as `refs/tags/<name>`. `sha` must be the full 40 chars for the server filter; shorter values are expanded with `GET R/git/commits/{sha}` first.
Every `ActionRun` response carries the embedded repository and trigger_user objects and `event_payload`: decode only the fields in 6.9.

#### `run`
Params: `action`, `repo`, `run` int, `number` int, `job` int, `name` (job name selector with `run`), `step` string (`N` or `failed`; for `log`; omitted + failed job = `failed`), `tail` int (default 80), `first` int (head lines), `grep`, `ignore_case` bool, `attempt` int, `max_chars` int (default 8000, ceiling 100000), `offset` int (follow mode), `status` string[] (enum the 8 words), `workflow` (file name), `ref`, `sha`, `pr` int, `event`, `inputs` object (`map[string]any`; values are stringified), `wait` bool, `timeout` int, `id` int (artifact id), `name` also used for artifact name filter, `save_to`, `include` string[] enum `steps|jobs`, `verbose`, `page`, `limit`.

| action | mode | required / optional | endpoints | output |
|---|---|---|---|---|
| `list` | R | status, workflow, ref, sha, pr, event, number; pg (limit default 10) | `GET R/actions/runs?status=..&workflow_id=&ref=&head_sha=&event=&run_number=&page&limit` (`status` repeated). Total from body `total_count` | run lines |
| `get` | R | run \| number; include (default `jobs`; `steps` adds steps of non-successful jobs); verbose | `GET R/actions/runs/{run}` + `GET R/actions/runs/{run}/jobs` (+ `GET R/actions/jobs/{job}` per non-successful job when `steps`) | run block + job lines (example below) |
| `job` | R | job | `GET R/actions/jobs/{job}` | job + step lines with durations |
| `log` | R | job \| (run + name); step, tail, first, grep, ignore_case, attempt, max_chars, offset | 10.2 (uses `GET R/actions/jobs/{job}/logs` with `Range`/`step`/`q`/`qi`/`attempt`; `GET R/actions/jobs/{job}` for step `failed`). When `step` is omitted and the job status is `failure`, the default is `step=failed` (the whole-log tail of a failed job is mostly post/cleanup noise); other jobs: whole-log tail. Implemented by `ci.FetchJobLog` | cleaned log text; the header names the chosen step |
| `cancel` | W | run \| number | `POST R/actions/runs/{run}/cancel` (204; silent no-op on finished runs) then `GET` the run and report the real status | `ok: run 1234 cancelled` or `ok: run 1234 already success (cancel was a no-op)` |
| `rerun` | W | run \| number | `POST R/actions/runs/{run}/rerun` (204, same run id, attempts bumped); pre-check run is done (server 400 otherwise); then `GET` run | `ok: rerun started: run 1234 running` |
| `rerun_job` | W | job | `POST R/actions/jobs/{job}/rerun` (also reruns dependents) | ok |
| `delete` | W | run \| number | pre-check `GET` run is done (else local error: server maps to 500); `DELETE R/actions/runs/{run}` (repo admin) | ok |
| `dispatch` | W | workflow, ref (default: repo default branch); inputs, wait, timeout | `POST R/actions/workflows/{workflow}/dispatches` `{ref, inputs, return_run_info:true}` -> 201 `{id, run_number, jobs}`; 204 (older servers) -> poll `GET R/actions/runs?workflow_id=&event=workflow_dispatch&limit=3` for 10 s and take the newest created after the call. Input values are stringified (`true` -> `"true"`). 400 messages are shown verbatim (`input required for ...`). `wait=true` continues into `ci.WaitRun` (`shared/ci/wait`, the same code `ci_wait` uses) with `timeout` (default `FORGEJO_WAIT_DEFAULT`, capped by `FORGEJO_WAIT_MAX`) | `ok: dispatched ci.yml on main: run 1235 #57 jobs: build,test` |
| `workflows` | R | ref; verbose | no API: `GET R/contents/.forgejo/workflows?ref=` first; only when that directory is 404 `GET R/contents/.gitea/workflows?ref=` (Forgejo Actions uses `.gitea/workflows` only when `.forgejo/workflows` is absent, and never runs `.github/workflows`, so those files are not listed: they would be dispatch targets that 404; the `[v]` item in 15 confirms the fallback order); each `*.yml|*.yaml` via `GET R/raw/<path>?ref=` (parallel <= 6), parsed with `yaml.v3`; cached per (repo, ref sha) | `ci.yml \| "CI" \| on: push,pull_request,workflow_dispatch \| inputs: env(choice dev\|prod, required, default dev), debug(boolean, default false)` |
| `artifacts` | R | run; name; pg | `GET R/actions/runs/{run}/artifacts` or `GET R/actions/artifacts` (`name` filter) | artifact lines |
| `artifact_download` | R | id, save_to | `GET R/actions/artifacts/{id}/zip` through `Client.Download` to `save_to` (rules 10.5). v4 artifacts answer 302 to a presigned storage URL on another host when the server uses MinIO `ServeDirect`; that redirect is followed and Authorization is dropped on the cross-host hop (5.2) | `ok: saved path (1.2MB)` |
| `artifact_delete` | W | id | `DELETE R/actions/artifacts/{id}` | ok |

Example (`run list limit=3`):
```
# runs 3/58 page 1/20 next: page=2
run 1236 no.58 running | ci.yml | pull_request #12 8a1b2c3d | by alice | 41s | 2026-11-22 10:15
run 1234 no.56 failure | ci.yml | push main 77aa11bb | by alice | 1m03s | 2026-11-22 09:50
run 1230 no.55 success | release.yml | push v1.2 5e5e5e5e | by bob | 4m10s | 2026-11-21 18:02
```
Example (`run get run=1234`):
```
run 1234 no.56 failure | ci.yml "CI"
push main 77aa11bb by alice | 2026-11-22 09:50 | 1m03s
url: https://forgejo.example.com/o/r/actions/runs/56
job 5678 build failure a1 on:docker
job 5679 lint success a1 on:docker
job 5680 test skipped a1 needs:build on:docker
```
Example (`run log job=5678 step=failed`) is in 10.2.

#### `ci_failure`
Params: `repo`, `run`, `number`, `pr`, `ref`, `sha`, `tail` int (default 30 lines per failing step), `max_jobs` int (default 5), `max_chars` int (default 12000, split across the failed jobs), `include_cancelled` bool, `attempt` int.
Mode R, no `action` param (single purpose). Target: `run` | `number` | `pr` (head sha from `GET R/pulls/{pr}`) | `sha` | `ref` | none (= target rule of 3.1 step 4; first output line `target: ...`). Runs are selected WITHOUT a status filter and every failed workflow is reported, not only the newest failed run, so the tool works right after a fail-fast `ci_wait` (the run is still `running`) and when several workflows fail; algorithm and output in 10.4. When no run exists: `no Actions run for <target>; latest: ...` (one `limit=1` query). Endpoints: `GET R/pulls/{n}`, `GET R/actions/runs`, `GET R/actions/runs/{id}`, `GET R/actions/runs/{id}/jobs`, `GET R/actions/jobs/{id}`, `GET R/actions/jobs/{id}/logs`, `GET R/commits/{ref}/status` (fallback for external CI contexts).

#### `ci_wait`
Params: `repo`, `run`, `number`, `pr`, `ref`, `sha`, `timeout` int seconds (default `FORGEJO_WAIT_DEFAULT`=50, hard cap `FORGEJO_WAIT_MAX`=55 unless the owner raised it for a long-timeout client; the call always returns before the cap with a resume line), `fail_fast` *bool (default true), `with_failure` *bool (default true: on failure append the `ci_failure` digest with 10-line tails). Mode R, no `action` param. Target: explicit selector, else the target rule of 3.1 step 4 (first output line `target: ...`). Semantics in 10.1. Output examples in 10.1.

#### `checks`
Params: `action`, `repo`, `ref`, `sha`, `state` (filter/list; set: `pending|success|error|failure|warning|skipped`), `context`, `description`, `target_url`, `verbose` bool, `page`, `limit`. There is no `pr` param: for a PR use `pr get` (ci line, blocked_by) or `ci_wait pr=N`; `checks` is for refs, shas and external statuses.
| action | mode | endpoints | output |
|---|---|---|---|
| `get` | R | target = `sha` \| `ref` \| none (= target rule of 3.1 step 4; first output line `target: ...`); `ci.CombinedStatus` (`GET R/commits/{ref}/status`, all pages); no statuses -> state `none` (server returns an empty object). `verbose` adds run summary `GET R/actions/runs?head_sha=` | `state: failure (3/4 ok) sha 8a1b2c3d` then failing and pending contexts one per line; all-green shows only the state line unless `verbose` |
| `list` | R | `GET R/commits/{ref}/statuses?state=&page&limit` (full history, noisy) | status lines |
| `set` | W | params: sha or ref (resolved to a full sha via `GET R/git/commits/{ref}`), state, context; description, target_url; `POST R/statuses/{sha}` `{state,context,description,target_url}` | `ok: status ci/external=success on 8a1b2c3d` |

#### `actions_config`
Params: `action`, `repo`, `org`, `scope` enum `repo|org|user` (default repo; org when `org` given), `kind` enum `secret|variable` (required), `name`, `value`, `page`, `limit`.
Prefix: repo `R`, org `/orgs/{org}`, user `/user`. Actions:
- `list` R: `GET {prefix}/actions/secrets` (names only) / `GET {prefix}/actions/variables`; pg.
- `get` R (variables only; secrets are write-only -> error): `GET {prefix}/actions/variables/{name}`.
- `set` W: secret -> `PUT {prefix}/actions/secrets/{name}` `{data: value}` (201 created / 204 updated); variable -> `GET .../variables/{name}`: 404 -> `POST` `{value}` (create), 200 -> `PUT` `{value}` (update). Names are uppercased by the server; output shows the final name. Value never echoed for secrets (`ok: secret FOO set`), variable echoes first 40 chars.
- `delete` W: `DELETE {prefix}/actions/secrets|variables/{name}`.
Needs: repo owner / org owner / the user themself (a collaborator with write is not enough).

### 8.7 runners (internal/tools/runners)

#### `runner`
Params: `action`, `scope` enum `repo|org|user|admin` (default repo), `repo`, `org`, `id` int, `name`, `description`, `ephemeral` bool, `visible` *bool (default true), `status` enum `online|offline|idle|active` (client filter), `label` (client filter), `labels` (queue filter, csv; for `register`: labels written into the printed `.runner` snippet), `page`, `limit`.
Prefix: repo `R/actions/runners`, org `/orgs/{org}/actions/runners`, user `/user/actions/runners`, admin `/admin/actions/runners`. Repo scope requires repo owner (`reqOwner`), org scope org owner, admin site admin; the tool puts that in 403 hints.
| action | mode | endpoints | output |
|---|---|---|---|
| `list` | R | `GET {prefix}?visible=<true>&page&limit` (visible=true by default: repo -> repo+owner+global, org/user -> owner+global, admin -> everything; `visible=false` only the exact level). `status`/`label` filters are client-side (header says so). Online = `idle` or `active` | runner lines; header notes `visible=true` |
| `get` | R | `GET {prefix}/{id}` | runner block (uuid, labels, version, description) |
| `register` | W | params: name; description, ephemeral, labels; `POST {prefix}` `{name, description, ephemeral}` -> 201 `{id, uuid, token}`. `labels` is only used to render the snippet (labels are declared by forgejo-runner, not by this call) | block below, including a ready-to-write `.runner` JSON line and the daemon command; the token is printed once, never cached, never logged |
| `delete` | W | params: id; `DELETE {prefix}/{id}` (404 `No permission to delete this runner` when the runner belongs to another level) | ok |
| `queue` | R | params: labels; `GET {prefix}/jobs?labels=` (waiting + running jobs, unpaginated) | `waiting: 2, running: 1 \| by runs-on: docker 2, ubuntu 1` then up to 20 job lines (`job 5678 build waiting on:docker repo_id:12`) |
| `reg_token` | W | params: -; `GET {prefix}/registration-token` (deprecated since Forgejo 15 but present in v17; creates a token as a side effect, hence Write) | `token: ...` + `note: deprecated; prefer register` |

Example (`runner register scope=repo name=ci-1 labels=docker:docker://node:20`):
```
ok: registered runner 7 "ci-1" (repo scope)
uuid: 3f2b...-...
token: 40-hex-chars-shown-once
address: https://forgejo.example.com
.runner: {"id":7,"uuid":"3f2b...-...","name":"ci-1","token":"40-hex-chars-shown-once","address":"https://forgejo.example.com","labels":["docker:docker://node:20"]}
start: write the .runner line to <dir>/.runner, set runner.file: .runner in config.yml, then run: forgejo-runner daemon --config config.yml
note: store the token now; shown once
```
The `.runner` line comes from one function (`runners.renderRunnerFile`) that the e2e test (12.2 step 2) also uses, so the text the tool prints is exactly what the integration test writes and starts a runner with; its schema is `[v]` (15.1). Without `labels` the key is omitted and the output adds `note: pass labels=a,b to include labels in the snippet`.

Runner line: `runner 7 ci-1 idle | repo | v6.3.1 | [docker,ubuntu-latest] | eph`. `scope` word: `repo` when `repo_id != 0`, `owner` when `owner_id != 0`, else `global`.

### 8.8 settings (internal/tools/settings)

#### `hook`
Params: `action`, `scope` enum `repo|org|user|admin` (default repo), `repo`, `org`, `id`, `type` enum `forgejo|gitea|gogs|slack|discord|dingtalk|telegram|msteams|feishu|wechatwork|packagist` (default forgejo), `url`, `content_type` enum `json|form` (default json), `secret`, `events` string[], `branch_filter`, `authorization_header`, `active` *bool, `ref`, `page`, `limit`.
Prefix: `R/hooks`, `/orgs/{org}/hooks`, `/user/hooks`, `/admin/hooks`. Actions: `list` R, `get` R, `create` W (`POST {prefix}` `{type, config:{url, content_type, secret}, events (default ["push"]), branch_filter, authorization_header, active (default true here; the server default is false)}`), `edit` W (read-modify-write: `GET {prefix}/{id}`, merge the given fields over it, send full `events`, `branch_filter`, `authorization_header`, `config.url`, `config.content_type`, `active` with `PATCH`, because the server resets omitted `events` to `["push"]` and clears omitted header/filter; an empty `authorization_header` is rejected locally (`error: invalid: the authorization header cannot be cleared via API on this server; delete and recreate the hook`) because Forgejo 17.0.2 keeps the old value on an empty PATCH; `secret` is NOT editable: `editHook` ignores it (it is applied at create only), so a `secret` given on edit is rejected locally with `error: invalid: hook secret cannot be changed via API; delete and recreate the hook`), `delete` W, `test` W (`POST R/hooks/{id}/tests?ref=` repo only; output `ok: test queued (result is not visible via API)`). Secrets and authorization headers are never echoed.

#### `access`
Params: `action`, `repo`, `kind` enum `collaborator|key` (required), `user`, `permission` enum `read|write|admin` (default write), `id`, `title`, `key`, `read_only` *bool (default true; server default is read-write), `page`, `limit`.
| action | mode | collaborator endpoints | key endpoints |
|---|---|---|---|
| `list` | R | `GET R/collaborators` | `GET R/keys` (`key_id`, `fingerprint` not exposed) |
| `permission` | R | `GET R/collaborators/{user}/permission` -> `alice: write (role)` | - |
| `add` | W | `PUT R/collaborators/{user}` `{permission}` | `POST R/keys` `{title,key,read_only}` |
| `remove` | W | `DELETE R/collaborators/{user}` | `DELETE R/keys/{id}` |

### 8.9 users (internal/tools/users)

#### `user`
Params: `action`, `user`, `q`, `sort`, `id`, `title`, `key`, `read_only`, `name`, `scopes` string[], `repos` string[] (limit token to repos), `page`, `limit`.
Actions: `me` R (`GET /user`), `get` R (`GET /users/{user}`), `search` R (`GET /users/search?q=` unwrap `{ok,data}`), `followers` / `following` R (`GET /users/{user}/followers|following`, or `/user/followers|following` when `user` empty), `starred` R (`GET /users/{user}/starred`), `orgs` R (`GET /users/{user}/orgs` or `/user/orgs`), `keys_list` R (`GET /user/keys`), `keys_add` W (`POST /user/keys` `{title,key,read_only}`), `keys_delete` W (`DELETE /user/keys/{id}`), `tokens_list` R (`GET /users/{user}/tokens`: id, name, scopes, last8), `tokens_create` W (`POST /admin/users/{user}/tokens` `{name, scopes, repositories:[{owner,name}]}` (tool param `repos` takes `owner/name` strings and is converted to `{owner,name}` objects); needs a site-admin token, otherwise `error: creating tokens needs a site-admin token or the web UI (the API requires the account password, which this server never accepts)`; scopes validated against `read|write:<activitypub,admin,issue,misc,notification,organization,package,repository,user>`, `all`, `public-only`; the secret is shown once), `tokens_delete` W (`DELETE /admin/users/{user}/tokens/{id}`).

#### `org`
Params: `action`, `org`, `user`, `team_id` int, `name`, `full_name`, `description`, `visibility` enum `public|limited|private`, `website`, `location`, `email`, `permission` enum `read|write|admin`, `units_map` object, `includes_all_repositories` *bool, `can_create_org_repo` *bool, `repo` (for team_add_repo: `owner/name`), `new_name`, `all` bool (list: `false` = my orgs, `true` = every org), `confirm`, `page`, `limit`.
Actions: `list` R (`GET /user/orgs`; `all=true` -> `GET /orgs`), `get` R (`GET /orgs/{org}`), `create` W (`POST /orgs` `{username,full_name,description,visibility,website,location,email}`), `edit` W (`PATCH /orgs/{org}`), `delete` D (`DELETE /orgs/{org}`, confirm=org), `rename` W (`POST /orgs/{org}/rename` `{new_name}`; for users the key is `new_username`, see admin `users_rename`), `members` R (`GET /orgs/{org}/members`), `member_remove` W (`DELETE /orgs/{org}/members/{user}`), `teams` R (`GET /orgs/{org}/teams`), `team_get` R (`GET /teams/{team_id}`), `team_create` W (`POST /orgs/{org}/teams`), `team_edit` W (`PATCH /teams/{team_id}`; name resent from a prior GET because swagger marks it required), `team_delete` D (`DELETE /teams/{team_id}`), `team_members` R (`GET /teams/{team_id}/members`), `team_add_member` W (`PUT /teams/{team_id}/members/{user}`; output warns `may send an email invite instead of adding`), `team_remove_member` W, `team_repos` R (`GET /teams/{team_id}/repos`), `team_add_repo` W (`PUT /teams/{team_id}/repos/{org}/{repo}`), `team_remove_repo` W.

#### `notify`
Params: `action`, `repo`, `id` (thread), `status` enum `unread|read|pinned|all` (list default `unread`), `types` string[] enum `issue|pull|repository`, `since`, `to` enum `read|unread|pinned` (default read), `page`, `limit`.
Actions: `count` R (`GET /notifications/new` -> `unread: 3`), `list` R (`GET /notifications`, or `GET R/notifications` when `repo` is given; `all=true` when status=all, else `status-types` repeated; `subject-type` repeated; number parsed from `subject.url`), `get` R (`GET /notifications/threads/{id}`), `mark` W (with `id`: `PATCH /notifications/threads/{id}?to-status=`; without: `PUT /notifications` (or `PUT R/notifications`) with `last_read_at=<time of this call>&to-status=&status-types=unread` ; the 205 body is read only to count; output `ok: marked 12 as read`).

### 8.10 packages (internal/tools/packages)

#### `package`
Params: `action`, `owner`, `type` enum `alpine|cargo|chef|composer|conan|conda|container|cran|debian|generic|go|helm|maven|npm|nuget|pub|pypi|rpm|rubygems|swift|vagrant`, `name`, `version`, `q`, `repo` (link target name), `confirm`, `page`, `limit`.
Actions: `list` R (`GET /packages/{owner}?type=&q=`), `get` R (`GET /packages/{owner}/{type}/{name}/{version}`), `files` R (`GET .../files`), `delete` D (`DELETE /packages/{owner}/{type}/{name}/{version}`, confirm=`owner/type/name/version`), `link` W (`POST /packages/{owner}/{type}/{name}/-/link/{repo}`), `unlink` W (`POST .../-/unlink`). `owner` defaults to the default repo's owner.

### 8.11 admin (internal/tools/admin; toolset `admin`, off by default)

#### `admin`
Params: `action`, `user`, `org`, `q`, `email`, `password` (never echoed, create/edit only), `full_name`, `login_name`, `source_id` int, `must_change_password` *bool, `admin` *bool, `active` *bool, `prohibit_login` *bool, `restricted` *bool, `visibility`, `purge` bool, `new_name`, `task` (cron), `pattern` (unadopted), `owner`, `repo`, `group`, `rule`, `limit_bytes` int, `subjects` string[], `groups` string[], `subject` (quota check), `confirm`, `page`, `limit`.
Actions (all need a site-admin token with `admin` scope; repo-restricted tokens cannot act as admin):
`users_list` R (`GET /admin/users?source_id=&login_name=&sort=`; no keyword filter: use `user.search`), `users_create` W (`POST /admin/users`), `users_edit` W (`PATCH /admin/users/{user}`; sends `login_name` and `source_id` together as the server requires), `users_delete` D (`DELETE /admin/users/{user}?purge=`, confirm=username), `users_rename` W (`POST /admin/users/{user}/rename` `{new_username}`: tool param `new_name` is sent under that key; note the org rename key is `new_name`), `orgs_list` R (`GET /admin/orgs`), `emails_list` R (`GET /admin/emails`), `emails_search` R (`GET /admin/emails/search?q=`), `cron_list` R (`GET /admin/cron`), `cron_run` W (`POST /admin/cron/{task}`; the server runs the task inline, so the request is sent from a detached context (10 min) with a progress notification every 10 s; after `FORGEJO_WAIT_MAX` s the tool returns `started; still running on the server; check with cron_list`), `unadopted_list` R (`GET /admin/unadopted?query=` the real param name), `unadopted_adopt` W (`POST /admin/unadopted/{owner}/{repo}`), `unadopted_delete` D (`DELETE /admin/unadopted/{owner}/{repo}`, deletes files), `quota_info` R (`GET /user/quota` or `/orgs/{org}/quota` or `/admin/users/{user}/quota`), `quota_groups` R (`GET /admin/quota/groups` or one by `group`), `quota_group_set` W (`POST /admin/quota/groups`), `quota_group_delete` W, `quota_rules` R (`GET /admin/quota/rules`), `quota_rule_set` W (`POST` or `PATCH /admin/quota/rules/{rule}`), `quota_rule_delete` W, `quota_user_groups_set` W (`POST /admin/users/{user}/quota/groups` `{groups}` replaces), `quota_check` R (`GET /user/quota/check?subject=`). Quota actions return `error: quota is disabled on this server` on 404. System webhooks are `hook scope=admin`; admin runners are `runner scope=admin`.

## 9. Generic escape hatch (internal/tools/meta)

### 9.1 `forgejo_api`
Mode: Write-capable via `Action.ModeFor` (4/5): classified per call (GET/HEAD read, other methods write, DELETE on destructive templates destructive); subject to read-only, deny and toolset rules by ENDPOINT (section 4 step 5), not only by tool name.
Params:
| name | type | meaning |
|---|---|---|
| `method` | enum `GET|POST|PUT|PATCH|DELETE|HEAD` | required |
| `path` | string | required; relative to `/api/v1` and starting with `/` (e.g. `/repos/o/r/topics`); `/forgejo/v1/...` selects the Forgejo-specific API; a pasted leading `/api/v1` is stripped; absolute URLs, `..`, `?` (use `query`) and `#` are rejected. Each segment is escaped keeping `/`, but sequences that are already valid percent-escapes (`%20`) are kept as given (no double escaping). The canonical path (`//` collapsed, trailing `/` stripped) is what the policy of section 4 step 5 evaluates. Only `/api/v1` and `/api/forgejo/v1` are reachable (package-registry upload paths under `/api/packages` are not) |
| `query` | object | string/number/bool/array values (arrays repeat the key) |
| `body` | any JSON | sent as JSON (`Content-Type: application/json`); a string body is sent raw as `text/plain` (for `/markdown/raw`) |
| `pick` | string[] | field paths to keep (9.3). Empty = whole (noise-stripped) response |
| `all_pages` | bool | follow `page` until the end (9.4), default false |
| `max_pages` | int | default 5, max 20 |
| `max_chars` | int | default 12000, ceiling 100000; an explicit value raises this call's output cap (6.6) |
| `raw` | bool | disable noise stripping |
| `save_to` | string | write the response body to this file instead of returning it (allowed root `FORGEJO_SAVE_DIR`, rules in 10.5; streamed through `Client.Download`) |
| `confirm` | string | required for destructive DELETE patterns (section 4 step 5f); equals the canonical `path` |
| `timeout` | int | seconds, default 30, max 600 |

Behaviour:
1. Validate; apply the policy of section 4 step 5 (canonicalise, match the endpoint index, deny/toolset/read-only/destructive rules); `repo` param does not exist here (the path is explicit).
2. Version/min checks do not apply; a 404 whose message is generic adds `hint: endpoint may not exist on this server (version X)` and, if `forgejo_endpoints` knows the path, `forgejo_endpoints lists it for >= major N`.
3. Response handling by `Content-Type`:
   - JSON: decode, drop noise (9.2, unless `raw`), apply `pick` (9.3), render as compact JSON (no spaces) preceded by a one-line header `# 200 GET /repos/o/r/topics 342B`. Lists: header `# 200 GET /... 20/134 items page 1 (more: page=2)` using `X-Total-Count` (or body `total_count`).
   - 204/205/201 with empty body: `ok (204)`.
   - text (diff, patch, raw, html, markdown): returned as text, cut at `max_chars` with the standard marker.
   - binary (zip, octet-stream, images): not returned; `binary <type> <N> bytes; pass save_to=<path>`; with `save_to`: `ok: saved <path> (<N>)`.
4. Errors: standard error line (section 11) plus the response `message`; non-JSON error bodies shown up to 300 chars.

### 9.2 Noise stripping (default)
Applied recursively: drop keys with value `null`, `""`, `[]`, `{}`; drop `url`, `avatar_url`, `*_url` except `html_url`, `target_url`, `clone_url`, `ssh_url`, `browser_download_url`, `download_url`; drop `permissions` maps that are all-false; collapse nested `user`/`owner`/`creator`/`author`/`doer`/`merged_by`/`trigger_user` objects to their `login` string; collapse nested `repository`/`repo` objects to `full_name`; drop `event_payload`. `false` and `0` are kept. Wrapper objects `{"ok":true,"data":[...]}` and `{"workflow_runs":[...],"total_count":N}` are unwrapped to the array with the total moved to the header.

### 9.3 `pick` syntax (jq-lite, `internal/format/pick.go`)
Grammar: `pick := expr ("," expr)*`; given as a JSON string array instead of comma list. `expr := path [":" alias]`; `path := seg ("." seg)*`; `seg := name | name "[]" | name "[" int "]"`. Root array -> applied per element, each element rendered as one JSON object per line (JSON Lines) so output stays greppable; root object -> one object. `labels[].name` yields an array of names. Missing paths are omitted (no nulls). `*` as a whole `seg` keeps all keys at that level (`user.*`). Examples: `["number","title","user.login:by","labels[].name:labels"]`. Invalid syntax -> `error: pick: <reason> at <col>`.

### 9.4 `all_pages`
Only for GET. Loop `page=1..`, `limit` = server max (`/settings/api`), stop when: fewer items than `limit`, accumulated >= `X-Total-Count`, `max_pages` reached, or output would exceed `max_chars`. Works for array bodies and for the unwrapped wrappers in 9.2 (including `{"workflow_runs":[...]}`); any other object body -> `error: all_pages needs a list response`. Header reports `pages: 3/5 fetched, 134 items, stopped: max_pages|end|chars`.

### 9.5 `forgejo_endpoints`
Params: `q` string (space-separated terms; all must match), `tag` enum (`activitypub|admin|issue|miscellaneous|notification|organization|package|repository|settings|user`), `method`, `detail` bool, `limit` int (default 15, max 50), `page` int.
Index record (`internal/endpoints/index.json`, array, compact keys): `{"m":"GET","p":"/repos/{owner}/{repo}/pulls","id":"repoListPullRequests","s":"List a repo's pull requests","t":"repository","q":[["state","e","open|closed|all"],["page","i"],...],"b":[["head","s",1],...],"sc":"repository","ov":"deprecated?"}` where `q` = query/path/header params `[name, type letter s|i|b|a|e|o, enumOrNote?]` (path params prefixed in `p` already), `b` = top-level body fields `[name, type, required 0|1]` resolved from `$ref`s one level deep, `sc` = token scope category from `scope.go` (rules in api-platform.md 4.x table), `dep` = true if deprecated. The generator also stores the Forgejo version (from the `-version` flag or `GET /version`, never from swagger `info.version`), the op count and the swagger sha256 in a header object `{"v":"17.0.2","n":512,"swagger_sha256":"...","ops":[...]}`; an op gets `"w":1` when its last path variable is multi-segment (`filepath`, branch/tag names), which `endpoints.Match` (4/5b) uses.
Search: lower-case terms; each term must be a substring of `op id + path + summary + tag + param names`; score = 4 for term in operationId, 3 in path, 2 in summary, 1 elsewhere, summed; sort by score desc then path; stable.
Output (`detail=false`; when a search returns 5 or fewer results every result also gets the compact parameter line shown under the first row, so the long-tail path is search -> api, not search -> search detail=true -> api):
```
# endpoints 3/512 (q="actions runners")
GET /repos/{owner}/{repo}/actions/runners | repoListActionRunners | List a repository's runners | tool: runner.list(scope=repo)
  query: visible(b) page(i) limit(i)
POST /repos/{owner}/{repo}/actions/runners | repoRegisterRunner | Register a runner | tool: runner.register(scope=repo)
  body: name* description ephemeral(b)
GET /orgs/{org}/actions/runners/{runner_id} | ... | tool: -
```
`tool:` = typed tool/action covering that endpoint (from `Action.Endpoints`, exact path templates), or `-`. `detail=true` adds per op: `query: state(open|closed|all) page limit | body: head* base* title*` and `scope: read:repository`.
Generation: `go generate ./internal/endpoints` runs `go run ./gen -in $SWAGGER -version $VER -out index.json` (flag `-in` accepts a file path or an `http(s)` URL of `/swagger.v1.json`; with a URL and no `-version` the version comes from `GET /version` of that server; with a file `-version` is required, because the swagger template's `info.version` is `{{AppVer}}` and the provided file says `x`; generation fails when the version is not semver; swagger `basePath` is ignored); the swagger file itself is not checked in (1+ MB), only `index.json` (~250 KB). WP0 commits a placeholder index (`{"v":"","n":0,"swagger_sha256":"","ops":[]}`) so `//go:embed` compiles before the first generation. `TestIndexHeader` checks that `v` is semver and `swagger_sha256` is 64 hex chars (CI cannot compare it with the unchecked-in swagger; the nightly integration job regenerates the index from the live server's `/swagger.v1.json` and fails on a diff). The test `TestIndexComplete` asserts count == 512 and that every `Action.Endpoints` entry exists in the index (typo guard), and `TestCoverageReport` logs the number of uncovered operations (informational).

## 10. Algorithms

### 10.1 Wait semantics (`ci_wait`, `run dispatch wait=true`; code in `internal/tools/shared/ci/wait`, owned by WP6b)
Poller: `Poll(ctx, step func(ctx) (done bool, snapshot Snapshot, err error), opts)`; all sleeping and deadlines go through `Env.Clock` (`internal/clock`, real/fake) so tests run with a fake clock.
- Interval: first poll immediately, then 2 s, 3 s, 4.5 s, then 6 s repeating (x1.5 up to 6 s; 10 s if the same snapshot repeated 5 times). `Retry-After`/429 respected via the client retry layer; a transient poll error (network/5xx after client retries) counts, 3 consecutive -> abort with the error.
- Timeout: `timeout` default `FORGEJO_WAIT_DEFAULT` (50 s), clamp to `[1, FORGEJO_WAIT_MAX]` (default 55 s; up to 600 only when the owner configured it for a client with a long tool timeout). The defaults stay under the 60 s request timeout many MCP clients apply (progress notifications extend it only when the client opts in), so the call returns a `timeout` verdict with a resume line instead of dying client-side. Deadline uses `context.WithTimeout` derived from the call context; cancellation of the call (client cancelled) aborts immediately and returns nothing. Every `timeout` result carries `resume: ci_wait <the ORIGINAL selector>` (e.g. `resume: ci_wait pr=42`, not `run=`), so a re-call also follows a later push.
- Progress: when the request carries a progress token, send `NotifyProgress` after every poll whose snapshot differs from the previous one: `progress` = elapsed seconds, `total` = timeout seconds, `message` e.g. `run 1234 running: 2/3 jobs done (build ok, test running)`. Without a token nothing is sent (never blocks on a failed notification).
- Resolution of the target on every poll (PR mode re-reads the head sha each poll so a push during the wait is followed; the result mentions `head moved 8a1b2c3d->9f9f9f9f`).
- Run mode (`run`/`number`): poll `GET R/actions/runs/{id}` + `GET R/actions/runs/{id}/jobs`. Done when status in {success, failure, cancelled, skipped}; with `fail_fast` (default) also done as soon as any job is `failure` (the run's remaining jobs are reported as `running`/`waiting`). Verdicts: `success`, `failure`, `cancelled`, `skipped`, `timeout`.
- Target mode (`pr`, `ref`, `sha`, none = the target rule of 3.1 step 4; the first output line is `target: ...` unless the selector was explicit): sha = PR head | resolve ref | branch head. Poll `ci.CombinedStatus(sha)` (all pages) and `GET R/actions/runs?head_sha=<sha>&limit=10`. Verdict rules, in order:
  1. PR mode: PR `merged` -> `merged` (with `merged as <sha8>`), PR closed unmerged -> `closed` (immediate, even if CI is pending).
  2. any status `failure|error` (or run `failure` when the run has no statuses) and `fail_fast` -> `failed`; with `fail_fast=false` wait until nothing is pending then `failed`.
  3. any run in `waiting|running|blocked`, or any status `pending` -> `pending`.
  4. combined `success` or `warning` and no unfinished run -> `green` (`warning` noted). PR mode with an auto-merge known to be scheduled (a 201 of `pr merge` remembered in `Env.Memo` key `automerge:o/r#n`, or the newest timeline event being `pull_scheduled_merge` without a later `pull_cancel_scheduled_merge` `[v]`, checked once when the verdict first turns green): do not return yet, keep polling for up to 30 s more until rule 1 applies (`merged as <sha8>`); if still unmerged after 30 s return `green` with `auto-merge pending`. This stops the agent from calling `pr merge` again and racing the server.
  5. no statuses and no runs: keep polling for a 20 s grace window (statuses appear when the run starts), then `no_checks`.
  6. deadline reached while `pending` -> `timeout` with the last snapshot and the `resume:` line.
- Stuck-queue diagnostic: when the verdict is `timeout` and any run/job is still `waiting`, add a `hint:` computed from `GET R/actions/runners?visible=true` (needs owner perms; ignored on 403): `no online runner covers runs-on [docker]; online runners: 1 [ubuntu-latest]` or `all runners offline`.
- Output (`ci_wait pr=42`, green). PR mode `green` always adds the `mergeable:` line from data `pr get` already computes (`blocked_by: approvals:1/2` instead of `ready: yes` when something blocks), so the agent knows whether `pr merge` will succeed without another call:
```
verdict: green | waited 48s | pr #42 head 8a1b2c3d
ci: success 4/4 | runs: run 1236 no.58 success 41s
mergeable: yes | ready: yes
```
  Failure with `with_failure` (digest of 10.4, 10-line tails):
```
verdict: failed | waited 31s | run 1236 no.58 (ci.yml, pull_request #42)
failed job 5678 "build" step 3 "Run tests"
  matches: 41: FAIL TestSessionRefresh (0.02s) | 44: session_test.go:44: expected 200, got 500
  --- tail (last 10 lines) ---
  ...
other: 1 pending (test)
```
  Timeout (the resume line echoes the original selector):
```
verdict: timeout | waited 50s | run 1236 no.58 running (build ok, test running)
resume: ci_wait pr=42
```

### 10.2 Job log reading (`run log`; shared with `ci_failure` and `ci_wait` through `ci.FetchJobLog` in `internal/tools/shared/ci`, cleaning in internal/format/log.go)
Facts (api-actions.md section 4): `GET R/actions/jobs/{job}/logs` supports `Range` (including suffix ranges) on the plain path, `step=N`, `attempt=N`; with `q`(+`qi=true`) or `format=ndjson` it streams the filtered body without range support. Stored lines look like `<RFC3339 timestamp>Z <content>` [v]. Expired/unexecuted logs -> 404 with messages `job has not been executed yet` / `logs have expired` (returned as a normal result, `IsError=false`: `no log: job has not been executed yet`).
Algorithm:
1. Select the job: `job`, or (`run` + `name`): `GET R/actions/runs/{run}/jobs` and match the job name (exact, then case-insensitive prefix; ambiguity lists candidates).
2. Select the step: `step=N` as given; `step=failed` -> `GET R/actions/jobs/{job}` and take the first step with status `failure` (else the last step that ran; none -> whole log); `step` omitted: when the job status is `failure` behave as `step=failed`, otherwise the whole log. Header uses the step name.
3. Mode by params:
   - `grep` set: `GET .../logs?q=<grep>&qi=<ignore_case>[&step=][&attempt=]`; read at most 4 MiB, clean lines (step 5), show up to 60 matches (`max_chars` also caps), header `matches 60 of 213 lines containing "x"`. Server gives no context lines; the output says `(no context available; use step/tail to read around)`.
   - `offset` set (follow mode): `Range: bytes=<offset>-`; output `next_offset: <total>`; 416 (no new data) -> `no new output; next_offset: <offset>`.
   - default (tail): request `Range: bytes=-<2*max_chars>` (suffix). Parse total from `Content-Range: bytes a-b/total` (206) or `len(body)` (200). If the range started inside a line (a > 0), drop the first partial line.
   - `first` set: `Range: bytes=0-<2*max_chars-1>` and keep the first `first` lines.
4. Clean (each line): strip a leading timestamp `^\d{4}-\d\d-\d\dT[\d:.]+Z ` ; strip ANSI (`\x1b\[[0-9;?]*[ -/]*[@-~]`); on `\r` keep the text after the last `\r`; drop `::group::`/`::endgroup::` markers but keep their title as a header `> title`; drop lines that are empty; collapse runs of identical consecutive lines into `line (x7)`.
5. Keep the last `tail` lines (default 80), then enforce `max_chars` from the end (`CapTail`: the beginning is cut at a line boundary, the end is kept). `FetchJobLog` also returns the cleaned lines of the fetched range so digests can scan them for error markers (10.4).
6. Output: header then text.
```
job 5678 "build" attempt 1 step 3 "Run tests" [failure] | 1.2MB total | last 80 lines
FAIL TestSessionRefresh (0.02s)
    session_test.go:44: expected 200, got 500
...
[truncated: 80 of 5142 lines, 6.0KB of 1.2MB; pass tail=N, grep=, or step=]
next_offset: 1260311
```
Memory: never read more than `max(4 MiB, 2*max_chars)` bytes of a log body.

### 10.3 PR merge flow (`pr merge`) (internal/tools/pulls/merge.go)
1. `GET R/pulls/{n}` (needed because several server failures are ambiguous). Local errors before any POST: merged -> `error: pr #n is already merged (merge_commit 8a1b2c3d)`; closed -> `error: pr #n is closed, not merged; reopen with pr edit state=open`; `draft` -> `error: pr #n is a draft (title prefix WIP); pr edit draft=false`; `head_sha` given and != head.sha -> `error: head moved (8a1b2c3d); re-check and retry`.
2. Method: param, else repo meta `default_merge_style` mapped to our enum (`rebase-update-only` -> `rebase`), else `merge`. Pre-check only `merge|rebase|rebase-merge|squash|fast-forward-only` against repo meta `allow_merge_commits|allow_rebase|allow_rebase_explicit|allow_squash_merge|allow_fast_forward_only_merge`; disallowed -> `error: style X disabled in this repo; allowed: merge,squash`. `manually-merged` is NOT pre-checked (the API repo object has no `allow_manual_merge` field; it exists only in the edit options, so a local check would always reject it): the server answers 405 `... is not an allowed merge style` and the tool passes it through (11.2).
3. `delete_branch` default = repo meta `default_delete_branch_after_merge` when the param is nil.
4. `POST R/pulls/{n}/merge` with exactly: `{"Do":..., "MergeTitleField":..., "MergeMessageField":..., "MergeCommitID":..., "head_commit_id":..., "force_merge":..., "merge_when_checks_succeed":..., "delete_branch_after_merge":...}` (omit empty). `when_green=true` sets `merge_when_checks_succeed`.
5. Outcomes:
   - 200 -> `GET R/pulls/{n}` -> `ok: merged pr #n as <merge_commit_sha8> (<method>)`.
   - 201 -> `ok: auto-merge scheduled for pr #n (merges when checks succeed; cancel with pr automerge_cancel)`; the tool calls `Env.Memo.Set("automerge:<owner/repo>#<n>")` so `ci_wait pr=n` keeps waiting for the actual merge (10.1 rule 4).
   - 405 with message `Please try again later` (conflict check still running): retry after 2 s, 4 s, 8 s (3 retries, then give up with the message).
   - 403 / 500 on the response while `delete_branch` was requested: `GET` PR; if `merged` -> `ok: merged pr #n as sha8; branch not deleted (<reason>)`; else the error below.
   - Other errors via the table in section 11.2 (empty 405 = `already merged`, 405 messages, 409 variants, 413, 422 `Do`, 423).
   - 409 handling, in order: (a) JSON with a `message` saying the merge is already scheduled (a second `merge_when_checks_succeed`) -> `error: 409 auto-merge already scheduled; pr automerge_cancel first`; (b) other 409 with a `message` -> pass through (11.2); (c) 409 with an empty or message-less JSON body: for `method=manually-merged` print `error: 409 merge_commit_id does not match a commit on the base branch` (the server serialises a plain Go error as `{}`); otherwise re-`GET` the PR: merged -> `ok: pr #n is already merged as <sha8>` (ErrPullRequestHasMerged returns a struct without `message`); else extract `CONFLICT (content): Merge conflict in <file>` lines from `StdOut`/`StdErr`/raw text, list at most 10 files: `error: merge conflict in a.go, b.go; update the branch (pr update) or resolve locally`.
6. On `merged` response when `when_green` and checks were already green the server merges immediately (200): same output as 200.

### 10.4 `ci_failure` algorithm (logic in `shared/ci/digest.go`, used by `ci_failure` and by `ci_wait with_failure`; thin tool in `internal/tools/ci`)
1. Resolve the target (explicit `run`/`number`, else `pr` (head sha from `GET R/pulls/{pr}`), `sha`, `ref`, else the target rule of 3.1 step 4; first output line `target: ...` unless explicit). For `run`/`number` the single run is used. For pr/sha/ref: `GET R/actions/runs?head_sha=<sha>&limit=20` WITHOUT a status filter (a fail-fast `ci_wait` reports `failed` while the run is still `running`; a `status=failure` filter would not find it). Group the runs by `workflow_id|event` and keep the newest run of each group.
2. A run counts as failed when its status is `failure` (`cancelled` only with `include_cancelled`), or when it is unfinished and any of its jobs (`GET R/actions/runs/{id}/jobs`) has status `failure`. Report EVERY failed group, not just one. Green groups collapse into one line `ok: lint.yml, docs.yml`; unfinished groups without a failure into `pending: test.yml`.
3. Failed jobs of a run: at most `max_jobs` (default 5, first by id; the rest summarised). Output budget: `max_chars` (default 12000, `Limit - 1500` at most) divided by the number of failed jobs over all failed runs (floor 600 each), instead of a fixed per-job size. For each failed job in parallel (errgroup, limit 4): `GET R/actions/jobs/{job}` -> first step with status `failure`, else the last step whose status != success; then `ci.FetchJobLog` (10.2, `step=<n>`, suffix range of `2*budget` bytes). If `task_id` is 0: `no log (job not started)`.
4. Digest per job: (a) scan the already fetched range client-side, case-insensitively for `error|fail|panic|exception|fatal|assert`, keep at most 12 matching lines with their line numbers (lines that only repeat the exit summary, e.g. `exit status 1`, are skipped) and print them first as `matches:`; (b) then the last `tail` lines (default 30; `ci_wait` uses 10) of the step. Identical tails across jobs (matrix) are printed once and later jobs say `same as job 5678`. The result is capped with `CapTail` per job so the END of each tail survives.
5. Fallback when no Actions run exists for the target: `GET R/commits/{sha}/status` (all pages); list failing contexts with their `target_url`: `no Actions run; external checks failing: ...`.
6. Output:
```
target: pr #42 (branch fix/login)
run 1234 no.56 failure | ci.yml | push main 77aa11bb | by alice | 2026-11-22 09:50
jobs: 1 failed (build), 1 ok (lint), 1 skipped (test)
failed job 5678 "build" step 3 "Run tests" (12s)
  matches: 41: FAIL TestSessionRefresh (0.02s) | 44: session_test.go:44: expected 200, got 500
  --- tail (last 30 lines) ---
  FAIL TestSessionRefresh (0.02s)
      session_test.go:44: expected 200, got 500
  exit status 1
run 1240 no.12 running | lint.yml | pull_request #42 | by alice | 2026-11-22 09:51
failed job 5702 "lint" step 2 "golangci-lint" (9s)
  same as job 5678
ok: docs.yml
```

### 10.5 Misc shared rules
- Repo meta fetch `GET R` is cached 5 min and shared by `pr merge`, `pr create`, `file patch/write`, `release`.
- Paths in URLs: every segment is escaped with `Seg()` (`url.PathEscape`). `Wild` (keeps `/`) is used ONLY for the multi-segment routes `/branches/*`, `/tags/*`, `/contents/*`, `/raw/*` (and variables flagged `w` in the endpoint index). Single-segment routes always take `Seg()` even when the value contains `/`: the branch-protection `rule` (`/branch_protections/{name}`: `release/*` -> `release%2F*`; `Wild` there would hit `/branch_protections/release/*` and 404) and the release `tag` (`/releases/tags/{tag}`). `owner`/`repo` are validated against `^[A-Za-z0-9._-]+$`.
- Local file arguments (`file`, `save_to`): allowed roots are `FORGEJO_SAVE_DIR` (default `os.TempDir()/forgejo-mcp`, created 0700) and, only with `FORGEJO_SAVE_CWD=1`, the process cwd. They are opened with Go's `os.Root` (`os.OpenRoot(root)`, then `root.OpenFile(rel, O_WRONLY|O_CREATE|O_EXCL)` for writes, `root.Open` for reads), which refuses `..`, symlink and junction escapes on every OS and works for files that do not exist yet (no `EvalSymlinks` plus prefix compare). A relative path is resolved against the first root; an absolute path must lie under a root after `filepath.Clean` (case-insensitive compare on Windows; UNC paths and drive-relative `C:foo` are rejected). Any path component starting with `.` (`.git`, `.claude`, `.vscode`, `.mcp.json`) is refused, as is the file named by `FORGEJO_TOKEN_FILE`. Downloads never overwrite (`error: exists: <path>`); uploads refuse files > 512 MiB. In HTTP mode these params are disabled unless `FORGEJO_HTTP_FILES=1`. The Docker image sets `WORKDIR /data` so the cwd is never `/`.
- Secrets never appear in output or logs: token, `password`, `secret`, `data` of secrets, `authorization_header`, runner token only in the register result.

## 11. Error mapping

### 11.1 Shape
Tool errors are returned as `CallToolResult{IsError:true}` with one text block; protocol errors (JSON-RPC) only for malformed requests the SDK rejects (schema validation). Format, always one to three lines:
```
error: <status> <reason>: <server message>
hint: <actionable next step>
at: <METHOD> <path>          (only on 5xx/unexpected, so the model can report it)
```
`<reason>` is the HTTP status text lowercased. The server's `url` field is dropped. The token never appears. Local validation errors use `error: invalid: <what>` (no status). Context deadline: `error: timeout after 30s calling GET /...`; connection failure: `error: cannot reach <host>: <cause>` (+ `hint: check FORGEJO_URL / network`).

### 11.2 Mapping table (`internal/forgejo/errors.go`; per-action hints come from `Action.Needs` and `Action.Hint500`)

| status | server message / situation | output |
|---|---|---|
| 401 | any | `error: 401 unauthorized: <msg>` hint `FORGEJO_TOKEN missing or invalid` (when no token configured: `set FORGEJO_TOKEN; anonymous access is read-only on public repos`) |
| 403 | `token does not have at least one of required scope(s): [read:x write:x]` | pass message; hint `create a token with scope write:x (read:x for reads)` (first scope of the bracket list) |
| 403 | `token scope is limited to public ...` | hint `token is public-only; use a token without public-only` |
| 403 | `Only administrators allowed to sudo.` / `user should be the site admin` | hint `needs a site-admin token` |
| 403 | `webhooks disabled by administrator`, `Only signed in user is allowed to call APIs.` | pass; hint feature disabled / sign in |
| 403 | other | pass message (or empty -> `no permission`); hint `needs: <Action.Needs>` (e.g. `repo owner`, `repo admin`, `write access`) |
| 404 | non-informative message: the generic `The target couldn't be found.`, or any `message` matching `^[A-Za-z]+$` (no spaces; the server sets `message` to a Go function name such as `GetPullRequestByIndex`, `GetContentsOrList`, `IsErrIssueNotExist` and puts the real error in the `errors` array) | `error: 404 not found: <errors[0]>` when `errors[0]` exists, else `error: 404 not found: <what we asked for in words>`; the function-name message is never shown; hint `check names; private resources also return 404 without access`; with MinMajor gap: `endpoint may not exist on Forgejo <server>` |
| 404 | `job has not been executed yet`, `logs have expired` (job logs) | not an error: `no log: <message>` |
| 404 | `pr merge` on closed-unmerged PR | prevented by the pre-check (10.3) |
| 405 | merge: empty message | `error: 405 pr already merged` |
| 405 | merge: `User not allowed to merge PR` / `Work in progress PRs cannot be merged` / `Does not have enough approvals yet` / `... required status ...` / signed-commit text | pass; hint per message: approvals -> `needs N approvals; see pr get`; status -> `wait for checks (ci_wait) or use when_green=true`; WIP -> `pr edit draft=false`; not allowed -> `needs write access or merge whitelist; force needs admin` |
| 405 | `Please try again later` | retried (10.3) then `error: 405 merge check still running` |
| 405 | `<style> is not an allowed merge style for this repository` | pass; hint allowed styles from repo meta |
| 409 | merge conflict (body without `message`, Go error struct) | `error: 409 merge conflict in <files>` (parse `CONFLICT (content): Merge conflict in <file>`); hint `pr update, or resolve locally` |
| 409 | merge with `merge_when_checks_succeed` when auto-merge is already scheduled (message present) | `error: 409 auto-merge already scheduled; pr automerge_cancel first` |
| 409 | `manually-merged` with a wrong `merge_commit_id` (`ctx.JSON(409, err)` serialises a plain error as `{}`, so there is no message) | `error: 409 merge_commit_id does not match a commit on the base branch` |
| 409 | merge, any other empty or message-less JSON body (ErrPullRequestHasMerged returns a struct) | re-GET the PR: merged -> `ok: pr #n is already merged as <sha8>`; else the conflict parse above, else the first 300 chars |
| 409 | `head out of date`, `merge push out of date` | pass; hint `re-fetch the PR and retry` |
| 409 | `PushRejected with remote message: ...` | pass the remote message |
| 409 | PR/release/branch/tag/repo already exists | `error: 409 exists: <message>`; typed actions add the existing id when cheap to find (pr: number) |
| 409 / 400 / 422 | contents sha or commit mismatch: **409** on `file write` and `file commit` (POST/PUT contents), **400** on `file delete` (DELETE contents), **422** on `file patch` (diffpatch) | hint `file changed since read; re-read and retry` (all three statuses) |
| 412 | edit/close PR already merged or deps open | pass; hint `merged PRs cannot be reopened` / `close dependent issues first` |
| 413 | quota exceeded | `error: 413 quota exceeded`; hint `free space or raise the quota (admin quota_*)` |
| 422 | binding / validation `[Field]: reason` | `error: 422 invalid: [Field]: reason` (first error only, as the server sends) |
| 422 | topics | list `invalidTopics` |
| 422 | `pr update` on a merged/closed PR: the handler calls `ctx.Error(422, ..., nil)`, so the message is the literal `%!s(<nil>)`, not empty | matched on `pr.update` by status 422 alone (or by that literal): `error: 422 pr is merged or closed; nothing to update` |
| 423 | archived | `error: 423 repo is archived`; hint `repo edit set={archived:false}` |
| 429 / 502 / 503 / 504 | after client retries | `error: <status> server busy/unavailable (retried 2x)` |
| 301/307/308 on non-GET | renamed | `error: moved to <path>`; hint `repo/user was renamed; use the new name` |
| 500 | message empty (production hides it for non-admins) | `error: 500 server error (message hidden by server)`; `hint: likely: <Action.Hint500>; else check server logs` plus `at:` line. Known `Hint500` texts: pr delete run not done (`run still active`); branch create with bad `from` (`unknown from ref`); contents access without permission or on pull-mirror (`no write access / mirror`); label filter with non-numeric id; `delete_branch_after_merge` failure (`merged but branch not deleted`); `issue link` (`dependency already exists or would be circular`) |
| other 4xx/5xx | | `error: <status> <reason>: <message or first 300 chars of body>` |

Rules: (1) JSON bodies are parsed for `message`; a message is non-informative when it equals the generic 404 text or matches `^[A-Za-z]+$` (a Go function name): then `errors[0]` is shown when present, else the wording of the row; if `message` is absent but the body is JSON, show the first 300 chars compactly; (2) HTML bodies (proxy error pages) -> `error: <status> <reason> (non-API response from proxy)`; (3) a successful 2xx with an unexpected empty body for a read -> `error: empty response`; (4) redactions: any occurrence of the token string(s) of THIS request (the configured token and, in HTTP pass-through mode, that request's own token) in a message is replaced by `***`.

## 12. Test plan

### 12.1 Unit tests (no network; run in CI on linux/macos/windows)
- Layout: `_test.go` beside code; fixtures in `internal/<pkg>/testdata/`. Golden files `testdata/*.golden` compared with `testing` + `-update` flag (helper `internal/testutil.Golden(t, name, got)`; owned by WP0; golden files and recorded fixtures are pinned LF/binary by `.gitattributes`, so Windows CI keeps the `\r` and ANSI bytes the log tests rely on). Shared test helpers (signatures frozen in WP0, bodies in WP4): `testutil.NewEnv(t, handler http.Handler, opts...) *tools.Env` (httptest server, a `forgejo.Client` with an injected `HTTPClient` and a `clock.Fake`), `testutil.CallTool(t, env, spec, args) (text string, isErr bool)` over `mcp.NewInMemoryTransports`, `testutil.FakeClock`.
- `forgejo` client: `httptest.Server` fakes. Cases: two tokens against one fake server never share `me`, repo meta, labels or the file ETag cache and each is redacted separately; `Download` follows a cross-host redirect and drops Authorization on that hop, an asset URL on another host is never requested with the token; token file with UTF-8 BOM, UTF-16LE BOM, CRLF and a binary byte (config); auth header form; no `?token=`; redirect rules (GET follows same host, POST returns moved error); retry/backoff on 503 with `Retry-After` (fake clock, no real sleeps); 204/205 bodies; body size cap and `Truncated`; `X-Total-Count`/body `total_count` extraction; `Page.More` math; limit clamp from `/settings/api`; version parse (`17.0.2+gitea-1.22.0`, `17.0.2-12-abcdef+gitea-1.22.0`, garbage); error mapping table (every row of 11.2 is a table-test line: status + body -> exact output); empty 500 messages; token redaction.
- `format`: truncation (rune safety, marker text), `T/Dur/SHA`, `pick` (grammar, arrays, aliases, errors), noise stripping, log cleaning (timestamps, ANSI, `\r`, group markers, dedupe, tail, range-start partial line), diff splitter (renames, binary, no-newline marker, path globs, caps), golden renders for every object type in 6.9.
- `gitremote`: URL parser table + fuzz, config parser (insteadOf, includes ignored, worktree `.git` file, quoted names), host matching with `FORGEJO_SSH_HOSTS`, sub-path instances, detection failure message.
- `config`: env matrix, precedence (flag > env), token file, invalid toolset, read-only parsing.
- `tools` framework: schema generation (action enum equals allowed actions; `required` = `["action"]`; `additionalProperties == false`; every property description of a multi-action tool starts with its `[actions]` prefix), toolset/read-only/deny filtering matrix (every Write/Destructive action absent in read-only; tool with only writes not registered; `forgejo_api` method filter), help output golden, handler ordering (unknown action, missing required, min-version gate, destructive `confirm`), panic recovery, `TestSchemaBudget` (total `tools/list` JSON for all non-admin toolsets <= 60,000 bytes, each tool <= 6,000 bytes, descriptions <= 160 chars; these are initial values: WP4 first generates the real schemas of stub param structs for `pr`, `issue`, `repo` and `run` (about 45 properties each, with the action-prefixed descriptions), measures, and freezes the final numbers in `internal/tools/budget.go`; other WPs cannot change them, so rarely used params go to `forgejo_api` instead), `TestEndpointsExist` (every `Action.Endpoints` in index), `TestForgejoAPIPolicy` (section 4 step 5: slash variants `//repos/o/r/`, deny by endpoint incl. toolset off and `/admin/`, GET `registration-token` blocked in read-only, destructive confirm from templates, pasted `/api/v1` prefix), `TestAnnotations` (explicit `destructiveHint:false` on non-destructive write tools), `TestCap` (trailer lines survive `Cap`, `CapTail` keeps the end, `max_chars` raises the cap), error texts reach the model through the raw `AddTool` (hidden action -> `action X is disabled (...)`, unknown action lists valid ones).
- Per area (each WP): fake-server tests for every action: request method/path/query/body asserted exactly (including exact key casing for merge, `is_archived` resend for label edit, webhook read-modify-write, `return_run_info:true`, `Range: bytes=-N` on logs, `limit` always present on search, `stat=false&files=false&verification=false` on commit lists), plus rendered output golden. Fixtures are real responses recorded from the 17.0.2 integration instance by `go test ./test/integration -run Record -record` (strips tokens, emails, hostnames to `forgejo.example.com`).
- Quirk tests (named after research items): merge matrix (200, 201, 405 empty, 405 try later x3 then ok, 409 conflict parse, delete-branch 403 after merge, closed-unmerged pre-check), `pr edit` 201 success, review own-PR pre-check, `label edit` never resets archived, `milestone list` always sends state, `comment list` windowing, `notify mark` handles 205 body, `runner register` token appears once and not in any log buffer (test captures stderr logger), `actions_config set` create-vs-update split, `file write` create/update/409 flow, `release edit` empty-string rejection, `ci_wait` verdict table with fake clock (green, failed fail-fast, failed no-fast, pending->timeout with stuck-queue hint, no_checks after 20 s grace, merged, closed, head moved).
- Quirk tests (API-correctness review, Appendix A items A1-A25): `issue lock` body is `{reason}`; `pushmirror_add` always sends `interval` and maps `remote_username`/`remote_password`, rejects `use_ssh` + credentials; `release asset_upload url=` is multipart with an empty body; merge style pre-check never rejects `manually-merged`; `hook edit` rejects `secret`; timeline header uses the unknown-total form and `next:` follows the raw page; `repo search private=true` sends `is_private`, never `private`; `pr edit milestone=none` goes to `PATCH /issues/{n}`; `branch protect_set` adds `enable_push:true` for whitelist-only sets; `release`/`branch_protections` escape one segment (`release%2F*`); `repo migrate` sends `repo_name`/`repo_owner`/`mirror_interval`, `admin users_rename` sends `new_username`, `user tokens_create` sends `repositories:[{owner,name}]`; `pr get` reads all review pages; downloads follow the storage redirect without Authorization; 404 with a function-name message shows `errors[0]`; merge 409 variants (already scheduled, manually-merged `{}`, re-GET to merged); `pr update` 422 literal; sha-mismatch hints on 400 (`file delete`) and 422 (`file patch`); `commit list` lines carry no `+a -d`; `tag get` lightweight; `run workflows` directory order; `issue link` uses canonical owner/repo; `repo fork` does not poll; `pr get head=owner:branch` uses `pulls/{base}/{head}`; `CombinedStatus` pages beyond `limit`; `comment delete` 204 no-op detection; `ci_wait` fail-fast followed by `ci_failure` on the still-running run; `pr merge when_green` then `ci_wait pr=N` returns `merged`; endpoint generator version handling.
- MCP-level: in-memory client/server (`mcp.NewInMemoryTransports()`), call `tools/list` and a few `tools/call` through the real SDK to catch schema/validation mismatches; stdio smoke test builds the binary to `filepath.Join(t.TempDir(), "forgejo-mcp"+exeSuffix)`, runs it as a subprocess against an httptest server, performs initialize + tools/list + one call, asserts nothing but protocol JSON on stdout.
- Coverage gate: `go test -cover` informational; target >= 80% for `forgejo`, `format`, `gitremote`, `config`.

### 12.2 Integration tests (`test/integration`, real Forgejo 17.0.2)
Env: `FORGEJO_TEST_URL`, `FORGEJO_TEST_TOKEN` (admin token). Every test starts with `skipUnlessIntegration(t)`; unset -> `t.Skip`. Optional: `FORGEJO_TEST_RUNNER=1` (+ `FORGEJO_TEST_RUNNER_IMAGE`, `FORGEJO_TEST_DOCKER_NETWORK`) enables the runner e2e (needs the `docker` CLI). Tests drive the server through the real MCP stack (in-memory transport) so schemas, output and errors are all exercised; each test creates its own org/repo with a random suffix (`it-<rand>`) and cleans up in `t.Cleanup`.
Environment (`test/integration/`; the whole package skips, not fails, when `bash` or `docker` are missing, e.g. on Windows runners; `harness_test.go` with frozen helper signatures `call`, `mustOK`, `mustErr`, `golden`, `Record` is committed by WP0 with `t.Skip` bodies and WP17 only fills in bodies; area WPs use hand-written fixtures during Phase 1 and WP19 re-records them with `-record` in one pass):
- `docker-compose.yml`: service `forgejo` image `codeberg.org/forgejo/forgejo:17.0.2` [v: tag name], sqlite3, env `FORGEJO__security__INSTALL_LOCK=true`, `FORGEJO__server__ROOT_URL=http://forgejo:3000/`, `FORGEJO__server__DOMAIN=forgejo`, `FORGEJO__actions__ENABLED=true`, `FORGEJO__service__DISABLE_REGISTRATION=true`, `FORGEJO__api__ENABLE_SWAGGER=true`, `FORGEJO__repository__DEFAULT_BRANCH=main`, port `3000:3000`, healthcheck `curl -fsS http://localhost:3000/api/healthz`; named network `fmcp-it`.
- `bootstrap.sh` (bash): `docker compose up -d --wait`; `docker compose exec -T -u git forgejo forgejo admin user create --admin --username it-admin --random-password --email it-admin@example.invalid --must-change-password=false`; `TOKEN=$(docker compose exec -T -u git forgejo forgejo admin user generate-access-token --username it-admin --scopes all --raw --token-name it | tr -d '\r')` (`-T`: no TTY, so no stray CR in the token); prints `export FORGEJO_TEST_URL=http://localhost:3000 FORGEJO_TEST_TOKEN=...` (a random password is generated by the script and discarded; no credentials are committed). Also `teardown.sh`.
- `harness_test.go`: starts the in-process server with config from env, helper `call(t, tool, args) (text string, isErr bool)`, `mustOK`, `mustErr`, `golden` for stable parts, `Record` mode.
Coverage by area (file per area, owned by that area's WP): repos (create with auto_init, edit, topics, branches + protection round trip, tags + protection, release + asset upload/download, wiki CRUD, file write/read/ls/tree/commit/patch/archive, commit list/get/compare), pulls (create from branch, get with blocked_by, label by name, reviews with inline comment, request reviewer, merge for each enabled style on separate PRs, when_green on a PR with no checks (merges), delete_branch, update branch, files/diff caps, closed-unmerged merge error, already merged error), issues (CRUD, labels by name incl. unknown-name error, milestone, comment window, timeline, lock, dependencies both directions, reaction, attachment upload/download, search flags), actions (below), runners (repo/org/user/admin scopes: register -> list visible -> get -> queue -> delete; admin `visible` semantics), settings (webhook create default active, edit preserves events, test; collaborator add/permission/remove; deploy key), users (me, search, orgs, teams incl. invite warning, keys, notifications count/list/mark), packages (generic package upload done by the test harness with plain `net/http` PUT to `/api/packages/{owner}/generic/<name>/<version>/<file>` (outside `/api/v1`, so not reachable through `forgejo_api`), then checked through the `package` tool, list/get/files/delete), admin (users create/edit/delete with confirm, cron list, quota disabled message), meta (info, api GET/POST/pick/all_pages, endpoints search incl. `tool:` column), read-only mode (server restarted with `FORGEJO_READ_ONLY=1`: tools/list has no mutating actions; direct call of a hidden action is refused; `forgejo_api POST` refused).
**Actions e2e with a real runner** (`actions_e2e_test.go`, requires `FORGEJO_TEST_RUNNER=1`):
1. Create repo `it-ci-<rand>` and commit via `file commit` three files on `main`: `.forgejo/workflows/ci.yml` (`on: [push, workflow_dispatch]`; jobs `ok` (`runs-on: self-hosted`, step `run: echo hello`) and `bad` (`runs-on: self-hosted`, step `run: echo before-failure; exit 3`)), `.forgejo/workflows/manual.yml` (`on: workflow_dispatch: inputs: {env: {type: choice, options: [dev, prod], required: true, default: dev}, debug: {type: boolean, default: false}}`; one job echoing `${{ inputs.env }}`), `README.md`. The push creates a run that stays `waiting` (no runner yet): assert `run list`, `runner queue` (waiting >= 2) and `ci_wait timeout=5` -> `verdict: timeout` with the stuck-queue hint `no online runner`.
2. Register through our own tool: `runner register scope=repo name=it-runner`. Take the `.runner` line printed by the tool itself (call it with `labels=self-hosted:host`) and write it unchanged to `<tmp>/runner/.runner` after `chmod 0777` on `<tmp>/runner` and 0644 on the files (the runner image runs as a non-root uid, and `t.TempDir()` is 0700 and owned by the CI user); the line must have the shape of the JSON `{"id":<id>,"uuid":"<uuid>","name":"it-runner","token":"<token>","address":"http://forgejo:3000","labels":["self-hosted:host"]}` [v: exact file schema of the pinned forgejo-runner version; if the daemon rejects it, fallback in the harness: `forgejo-runner register --no-interactive --instance http://forgejo:3000 --token <runner reg_token via runner.reg_token> --name it-runner --labels self-hosted:host`] and `<tmp>/runner/config.yml` (`runner.file: /data/.runner`, `runner.capacity: 1`, `host.workdir_parent: /data/work`, `log.level: info`).
3. Start the container: `docker run -d --name fmcp-it-runner --network fmcp-it -v <tmp>/runner:/data $FORGEJO_TEST_RUNNER_IMAGE forgejo-runner daemon --config /data/config.yml` (image default `code.forgejo.org/forgejo/runner:<current stable>`, pinned in `test/integration/runner.version`; host-executed jobs, so no Docker-in-Docker and no actions checkout needed). Cleanup removes the container, the runner (`runner delete`), the repo.
4. Poll `runner list` until our runner is `idle|active` (<= 60 s).
5. `ci_wait sha=<head> timeout=100` (harness config sets `FORGEJO_WAIT_MAX=120`) -> verdict `failed`, returned as soon as job `bad` fails while the run is still `running` (job `bad` fails, `ok` succeeds). `ci_failure sha=<head>` called right after that fail-fast verdict (the run may still be `running`) -> output contains job `bad`, a step name and the line `before-failure`. `run get`, `run job`, `run log job=<bad> step=failed` (contains `before-failure`), `run log grep=hello job=<ok>`, `run log` with `tail=2`, `offset` follow returns `next_offset`.
6. `run rerun_job job=<bad>` then `ci_wait run=<id>`; `run cancel` on a freshly dispatched run (race-tolerant: accepts `already done`); `run delete` on a done run; `run dispatch workflow=manual.yml inputs={env: prod, debug: true} wait=true` -> verdict success and the run log contains `prod`; dispatch with a missing required input -> `error: 400 ... input required` message surfaced.
7. `run workflows` lists both files with the dispatch input schema; `run artifacts` empty; `checks get ref=main` reports `success|failure` consistent with the verdict; `pr create` from a branch then `ci_wait pr=<n>` -> verdict, `pr merge when_green=true` on a PR whose checks are green -> `merged` or `scheduled`; auto-merge `automerge_cancel`.
8. Teardown asserts the runner is gone (`runner list` no longer shows it) and no container remains.
Integration tests that need the admin scope are skipped with a clear message when the token lacks it (probed via `forgejo_info verbose`).

### 12.3 Manual smoke (release checklist, not automated)
`claude mcp add` against a real instance; ask for PR list, merge a green PR, `ci_failure` on a red run; Claude Desktop config load; `tools/list` token count measured with the SDK's `tokens` counter (record in CHANGELOG).

## 13. Build, CI and release files (owned by WP18)

- `go.mod`: `module github.com/AndreyQuantum/forgejo-mcp`, `go 1.25.0` (SDK minimum), requires `github.com/modelcontextprotocol/go-sdk v1.8.0`, `golang.org/x/sync`, `gopkg.in/yaml.v3` (pin current patch versions at implementation time).
- `Makefile` (created by WP0 with `build`/`test`, afterwards owned by WP18): `build`, `test`, `lint` (includes `golangci-lint config verify`), `integration` (runs bootstrap + `go test ./test/integration -count=1`), `endpoints SWAGGER=...`, `docs` (generates `docs/TOOLS.md` from the registry via `go run ./internal/tools/docgen`), `snapshot` (goreleaser snapshot).
- `.golangci.yml` (golangci-lint v2 format; created by WP0, afterwards owned by WP18): `version: "2"`; `linters.default: standard`; `linters.enable: [bodyclose, errorlint, misspell, noctx, nolintlint, unconvert, usestdlibvars]` (gofumpt and goimports are FORMATTERS in v2 and must not appear under `linters`); `linters.exclusions.rules`: `_test.go` for `errcheck` of `Close`; `formatters.enable: [gofumpt, goimports]`. CI runs `golangci-lint config verify` before linting.
- `.github/workflows/ci.yml` (on push to main and pull_request): jobs
  - `lint`: `actions/checkout@v4`, `actions/setup-go@v5` (`go-version: stable`), `golangci-lint config verify`, `golangci/golangci-lint-action@v8`.
  - `test`: matrix `ubuntu-latest, macos-latest, windows-latest` with `go-version: stable`; `go vet ./...`; `go test -race -count=1 ./...` (race only on linux/macos); uploads nothing. `.gitattributes` keeps goldens LF on Windows; the integration package skips itself there (no bash/docker). A separate job `test-min` runs `go test ./...` once on ubuntu with `go-version-file: go.mod` (the minimum Go).
  - `build`: matrix of `GOOS/GOARCH` cross-compiles with `CGO_ENABLED=0` (linux/darwin/windows x amd64/arm64) to catch platform build breaks.
  - `goreleaser-check`: `docker/setup-qemu-action` and `docker/setup-buildx-action` first (`dockers_v2` needs a buildx builder), then `goreleaser check` + `goreleaser release --snapshot --clean --skip=publish` (images are built locally, not pushed).
  - `integration` (only `workflow_dispatch` and schedule nightly, plus `push` to `main`): runs `test/integration/bootstrap.sh`, then `go test ./test/integration -count=1` with `FORGEJO_TEST_RUNNER=1`; regenerates `internal/endpoints/index.json` from the live server's `/swagger.v1.json` and fails on a diff; always runs `teardown.sh`; uploads `docker compose logs` on failure.
  Pin actions by tag now, by commit SHA before the first release (verify latest majors at implementation time).
- `.github/workflows/release.yml` (on tag `v*`): steps checkout (`fetch-depth: 0`), setup-go (`stable`), `docker/setup-qemu-action`, `docker/setup-buildx-action`, `docker/login-action` (registry `ghcr.io`, `GITHUB_TOKEN`), then `goreleaser/goreleaser-action@v6` with `args: release --clean`, permissions `contents: write, packages: write`. **Nothing is published by this design/implementation work: the workflow only runs when the owner pushes a tag.**
- `.goreleaser.yaml` (`version: 2`):
```yaml
project_name: forgejo-mcp
builds:
  - id: forgejo-mcp
    main: ./cmd/forgejo-mcp
    binary: forgejo-mcp
    env: [CGO_ENABLED=0]
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    flags: [-trimpath]
    ldflags: ["-s -w -X main.version={{.Version}} -X main.commit={{.ShortCommit}} -X main.date={{.Date}}"]
archives:
  - formats: [tar.gz]
    format_overrides: [{goos: windows, formats: [zip]}]
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    files: [LICENSE, README.md]
checksum: {name_template: checksums.txt}
changelog: {use: github, sort: asc, filters: {exclude: ["^docs:", "^test:", "^chore:"]}}
dockers_v2:   # verify key name against the goreleaser version pinned in CI; alternative: classic `dockers`
  - images: [ghcr.io/andreyquantum/forgejo-mcp]
    platforms: [linux/amd64, linux/arm64]
    tags: ["{{ .Version }}", "{{ if not .Prerelease }}latest{{ end }}"]   # prereleases never get `latest`
    dockerfile: Dockerfile
    labels: {"io.modelcontextprotocol.server.name": "io.github.AndreyQuantum/forgejo-mcp", "org.opencontainers.image.source": "https://github.com/AndreyQuantum/forgejo-mcp"}
release: {draft: true}     # owner publishes manually
```
- `Dockerfile` (the shape `dockers_v2` requires: its build context is laid out as `<os>/<arch>/<binary>`): a tiny build stage creates `/data` owned by `65532:65532`; final stage `FROM gcr.io/distroless/static-debian12:nonroot`, `ARG TARGETPLATFORM`, `COPY $TARGETPLATFORM/forgejo-mcp /forgejo-mcp`, `COPY --from=<stage> --chown=65532:65532 /data /data`, `WORKDIR /data` (the cwd is never `/`), `ENV FORGEJO_SAVE_DIR=/data/save`, `ENTRYPOINT ["/forgejo-mcp"]` (stdio default; `--transport http --addr 0.0.0.0:8787` needs `FORGEJO_MCP_BEARER`).
- `server.json` (official MCP registry; **not published**; validate with `mcp-publisher validate` if available, schema URL and exact field names to be confirmed against the registry docs at implementation time [v]):
```json
{
  "$schema": "<current registry schema URL>",
  "name": "io.github.AndreyQuantum/forgejo-mcp",
  "description": "Token-efficient MCP server for Forgejo: PRs, CI runs and logs, runners, issues, repos.",
  "repository": {"url": "https://github.com/AndreyQuantum/forgejo-mcp", "source": "github"},
  "version": "0.1.0",
  "packages": [{
    "registryType": "oci",
    "identifier": "ghcr.io/andreyquantum/forgejo-mcp:0.1.0",
    "transport": {"type": "stdio"},
    "environmentVariables": [
      {"name": "FORGEJO_URL", "description": "Forgejo base URL, e.g. https://forgejo.example.com", "isRequired": true},
      {"name": "FORGEJO_TOKEN", "description": "Personal access token (optional: anonymous access is read-only)", "isRequired": false, "isSecret": true},
      {"name": "FORGEJO_REPO", "description": "Default repo owner/name (optional)"},
      {"name": "FORGEJO_TOOLSETS", "description": "Comma list of toolsets (optional)"},
      {"name": "FORGEJO_READ_ONLY", "description": "1 to hide mutating actions (optional)"}
    ]
  }]
}
```
- `README.md` outline: one-paragraph pitch with a before/after token example; install (`go install github.com/AndreyQuantum/forgejo-mcp/cmd/forgejo-mcp@latest`, release binary, Docker); configuration table (section 3); token scopes needed (`read:repository write:repository read:issue write:issue read:notification write:notification read:organization write:organization read:user write:user read:package`; admin tools additionally `write:admin` + site admin; a repo-limited token works for repo tools); Claude Code: `claude mcp add forgejo -e FORGEJO_URL=https://forgejo.example.com -e FORGEJO_TOKEN_FILE=~/.config/forgejo-mcp/token -- forgejo-mcp` (recommended: a token file or the `.mcp.json` `${FORGEJO_TOKEN}` expansion; a literal `-e FORGEJO_TOKEN=...` is stored in plain text in `~/.claude.json`; also `--scope user|project`); Claude Desktop `claude_desktop_config.json` snippet (`"command": "/path/forgejo-mcp", "env": {...}`); toolsets table; tool table (generated); output format sample; read-only mode; HTTP mode and its security notes; compatibility (tested 17.0.2); troubleshooting (`--check`, `FORGEJO_DEBUG`, token scopes, 404 means no access); tool timeouts (`ci_wait` defaults to 50 s and is capped at 55 s so it returns before the 60 s request timeout common in MCP clients; to wait longer raise `FORGEJO_WAIT_MAX` AND the client limit, e.g. `MCP_TOOL_TIMEOUT` for Claude Code; uploads and long migrations are bounded by the same client limit); FAQ (why `action` enums, how to reach unlisted endpoints via `forgejo_endpoints` + `forgejo_api`); contributing/test instructions; license MIT. All examples use `forgejo.example.com`; no real hostnames or user names.
- `LICENSE` MIT (copyright holder name as the owner configures in the GitHub repo; implementers use `AndreyQuantum forgejo-mcp contributors`). `CHANGELOG.md` (Keep a Changelog). `docs/TOOLS.md` generated.

## 14. Implementation plan (work packages)

Rules for parallel agents: each WP owns the listed paths exclusively, and every Go package has exactly ONE owning WP (so a compile error or a helper-name clash in one agent's package can never break another agent's `go test`). No WP edits another WP's files, `go.mod`, or `all.go`. Logic that two areas need lives in `internal/tools/shared` (WP6) or `internal/tools/shared/ci` (WP6b), never in an area package; areas may import both (section 1). If a WP needs a contract change it stops and reports to the orchestrator (contracts and test-helper signatures are frozen after WP0). Every WP ends with: `gofumpt`-clean code, `go vet`, `go test ./<owned pkgs>/...`, golden/fake-server tests for everything it adds, and an `Endpoints` list on every Action (verified by the framework test).

Ownership fixes (explicit, to remove the overlaps found in review): `internal/format/diff.go` -> WP3; `internal/tools/shared/**` -> WP6; `internal/tools/shared/ci/**` -> WP6b; `Makefile` and `.golangci.yml` are created by WP0 and afterwards edited only by WP18; `.gitattributes`, `internal/clock/**`, `internal/testutil/golden.go` -> WP0; `internal/testutil/{env,call,fixtures}.go` signatures -> WP0, bodies -> WP4; `test/integration/harness_test.go` skeleton with frozen signatures and `t.Skip` bodies -> WP0, bodies -> WP17; `internal/endpoints/index.json` placeholder -> WP0, generated content -> WP5; `internal/tools/{instructions,budget,progress}.go` -> WP4; `internal/tools/meta/policy.go` -> WP5.

Dependency graph: `WP0 -> {WP1, WP2, WP3} -> WP4 -> {WP5, WP6, WP6b} -> {WP7..WP16} (all parallel) -> {WP17 e2e, WP19}`; `WP18` after WP4 (parallel with WP5..WP16); the WP17 harness bodies may start after WP4.

### Phase 0: core (sequential, then parallel)
| WP | Scope / files (exclusive) | Depends | Deliverable and acceptance |
|---|---|---|---|
| **WP0** scaffold + frozen contracts | `go.mod` (`go 1.25.0`), `go.sum`, `.gitignore`, `.gitattributes`, `LICENSE`, initial `Makefile` and `.golangci.yml`, `cmd/forgejo-mcp/main.go` (flags parse + TODO wiring), `internal/clock/**` (full, tiny), `internal/tools/{registry,call,schema,help,minver,instructions,budget}.go` (types and signatures from section 2, bodies `panic("WP4")` where logic is non-trivial), `internal/tools/all/all.go`, ALL area packages as stubs (`internal/tools/{meta,repos,content,pulls,issues,actions,ci,runners,settings,users,packages,admin}/register.go` calling per-tool `registerX(r)` funcs defined in per-tool stub files: `repo.go, branch.go, tag.go, wiki.go` (repos), `release.go, file.go, commit.go` (content), `pr.go, review.go`, `issue.go, comment.go, label.go, milestone.go, attach.go`, `run.go` (actions), `ci_failure.go, ci_wait.go, checks.go, actions_config.go` (ci), `runner.go, hook.go, access.go, user.go, org.go, notify.go, package.go, admin.go`), `internal/tools/shared/**` and `internal/tools/shared/ci/**` as signature stubs (function signatures of 6/6b so areas compile against them), `internal/tools/deps.go` (blank imports of every third-party dep so `go.mod` is final), `internal/testutil/{golden.go (full), env.go, call.go, fixtures.go (signatures)}`, `internal/endpoints/{index.json placeholder, endpoints.go, scope/scope.go}` stubs, `internal/forgejo/*.go` and `internal/format/*.go` type/func signatures with `TODO` bodies where WP2/WP3 fill in, `test/integration/harness_test.go` skeleton | none | `go build ./...` and `go vet ./...` pass; every contract in section 2 exists; each area has one stub file per tool so later WPs replace whole files only; the integration package compiles and skips. Single agent, ~half a day |
| **WP1** config + gitremote | `internal/config/**` (incl. `tokenfile.go`: BOM/UTF-16/CRLF handling), `internal/gitremote/**` | WP0 | section 3 and 3.1 fully implemented (default-target helper inputs: current branch, git-detected repo); table + fuzz tests; `--check` helper `config.Redacted()` |
| **WP2** forgejo client | `internal/forgejo/**` (client incl. `Download`, `HTTPClient`/`Clock` injection, errors per 11 incl. non-informative 404 rule, paths, page incl. unknown-total/raw-page-full rule, version, settings, token-namespaced cache, types incl. all projection structs of 6.9 that are shared across areas: User, Repo meta, Label, Milestone, Status) | WP0 | sections 5 + 11 behaviours; httptest suite incl. retry with fake clock, redirect rules, cross-host download without Authorization, two-token cache isolation, error table |
| **WP3** format | `internal/format/**` (text, time, log, pick, noise, diff) | WP0 | sections 6 (incl. `Cap`/`CapTail` helpers as pure functions `format.CapHead/CapTail`), 8.4 diff rules, 9.2-9.3, 10.2 steps 4-5, error-marker scan of 10.4; goldens |
| **WP4** registry + server wiring | `internal/tools/{registry,call,schema,help,minver,instructions,budget,progress}.go` bodies and tests, `internal/testutil/{env,call,fixtures}.go` bodies, `cmd/forgejo-mcp/main.go` (stdio + http transports with the timeouts/options of section 3, flags, `--check`, `--version`, signal handling, logging to stderr) | WP0, WP1, WP2, WP3 | sections 2, 4 (steps 1-4, 6, 4.1), 5.4; raw `Server.AddTool` registration with our own validation and error texts, per-property `[actions]` prefixes, explicit annotations, `ModeFor`; in-memory MCP test with a dummy spec; schema-budget measurement and frozen `budget.go`; stdio subprocess smoke test |
| **WP5** meta tools + endpoint index | `internal/endpoints/**` (gen with `-version`, `scope/`, index.json generated from the v17.0.2 swagger at `.../scratchpad/swagger.json` with `-version 17.0.2`, `endpoints.go` incl. `Match`), `internal/tools/meta/**` (incl. `policy.go` = section 4 step 5) | WP2, WP3, WP4 | `forgejo_info`, `forgejo_api`, `forgejo_endpoints` per 8.1 and 9; `TestIndexComplete` (512 ops), `TestIndexHeader`, `TestForgejoAPIPolicy`; `covered_by` wiring via registry |
| **WP6** shared helpers | `internal/tools/shared/**` (not `shared/ci`): repo-meta fetch, label/milestone name->id resolvers with cache, ref normaliser (3.1 branch, 8.6 refs), `ResolveTarget` (3.1 step 4), run resolver (`run\|number\|pr\|ref\|sha`), `AllReviews`, `ParseOwnerRepo`, `AssetPath` (5.7), `OpenLocal`/`SaveFile` (10.5, `os.Root`) | WP2, WP4 | used by area WPs; unit tests with fake server |
| **WP6b** CI plumbing | `internal/tools/shared/ci/**`: `CombinedStatus` (paged), `FetchJobLog` (10.2), run/job/step line renderers (6.9), `FailureDigest` (10.4), `wait/` (`Poll`, `WaitRun`, `WaitTarget`, verdict rules and the auto-merge follow-up of 10.1) | WP2, WP3, WP4 | fake-clock tests for every verdict row (green, failed fail-fast, failed no-fast, pending -> timeout with stuck-queue hint and original-selector resume line, no_checks after 20 s grace, merged, closed, head moved, green + scheduled auto-merge -> merged); log golden tests; digest golden tests incl. matrix dedupe and multi-workflow output |

### Phase 1: areas (all parallel after WP5, WP6 and WP6b)
| WP | Tools / files (exclusive) | Notes |
|---|---|---|
| **WP7** repos | `repo`, `branch`, `tag`, `wiki`: `internal/tools/repos/**` (own `project.go`), `test/integration/repos_test.go` | section 8.2 |
| **WP8** content | `release`, `file`, `commit`: `internal/tools/content/**` (own `project.go`), `test/integration/content_test.go` | uses `format/diff.go`; ETag read cache lives in `file.go`; asset download via `shared.AssetPath` + `Client.Download` |
| **WP9** pulls | `pr`, `review`: `internal/tools/pulls/**`, `test/integration/pulls_test.go` | 8.4 + 10.3; the merge matrix tests are mandatory; uses `ci.CombinedStatus`, `shared.AllReviews`, `Env.Memo` |
| **WP10** issues | `issue`, `comment`, `label`, `milestone`, `attach`: `internal/tools/issues/**`, `test/integration/issues_test.go` | 8.5; name resolvers from WP6 |
| **WP11** actions | `run` (incl. `workflows`, artifacts, log, dispatch): `internal/tools/actions/**`, `test/integration/actions_run_test.go` | 8.6, 10.2; thin: calls `ci.FetchJobLog`, `ci` renderers and `ci.WaitRun` from WP6b |
| **WP12** ci | `ci_failure`, `ci_wait`, `checks`, `actions_config`: `internal/tools/ci/**`, `test/integration/ci_test.go` | 10.1 and 10.4; thin tool wrappers over `shared/ci`; target selection via `shared.ResolveTarget` |
| **WP13** runners | `runner`: `internal/tools/runners/**`, `test/integration/runners_test.go` | 8.7 (`renderRunnerFile` is shared with the WP17 e2e through an exported test helper) |
| **WP14** settings | `hook`, `access`: `internal/tools/settings/**`, `test/integration/settings_test.go` | 8.8 (webhook read-modify-write; secret not editable) |
| **WP15** users | `user`, `org`, `notify`: `internal/tools/users/**`, `test/integration/users_test.go` | 8.9 |
| **WP16** packages + admin | `package`, `admin`: `internal/tools/packages/**`, `internal/tools/admin/**`, `test/integration/admin_test.go` | 8.10, 8.11 |
| **WP18** release engineering | `.github/**`, `.goreleaser.yaml`, `Dockerfile`, `server.json`, `README.md`, `CHANGELOG.md`, `internal/tools/docgen/**`, and from now on `Makefile` and `.golangci.yml` | section 13; README written against the final tool list; no publishing |

### Phase 2: verification
| WP | Scope | Depends |
|---|---|---|
| **WP17** integration harness | `test/integration/{docker-compose.yml,bootstrap.sh,teardown.sh,harness_test.go (bodies),runner.version}`, `actions_e2e_test.go` (runner e2e, 12.2), record mode | harness bodies may start after WP4 (needs only `forgejo_info`); the e2e after WP11-13 |
| **WP19** full run + review | run the whole integration suite against Forgejo 17.0.2; resolve every `[v]` item in section 15 (document results in `docs/VERIFIED.md`); re-record fixtures and regenerate goldens in one pass; review of each area against this design; token-budget measurement; fixes | all |

Ordering summary for the orchestrator: (1) WP0; (2) WP1, WP2, WP3 in parallel; (3) WP4; (4) WP5, WP6, WP6b, WP18 in parallel; (5) WP7-WP16 in parallel (10 agents), WP17 harness bodies in parallel; (6) WP17 e2e; (7) WP19. Reviews after each phase boundary: after (3) review contracts vs section 2; after (5) review each area WP diff.

## 15. Items to verify on a live 17.0.2 instance (WP19, marked `[v]` above)

Status after the WP19 run: every item is answered in [VERIFIED.md](VERIFIED.md) (verified, partly verified, or explicitly open). The `[v]` markers above remain as pointers to those rows.
1. Docker image tag format `codeberg.org/forgejo/forgejo:17.0.2` and the forgejo-runner image/tag; exact `.runner` JSON schema accepted by the pinned runner; whether labels from `.runner` or `config.yml` win.
2. Stored job-log line format (timestamp prefix) and `Content-Range` behaviour of suffix ranges when the log is smaller than the range; whether archived `.log.zst` logs support Range cheaply.
3. 409 merge-conflict body shape; `Mergeable` semantics while checking; files totals cap in `/pulls/{n}/files`; `/commits` order; whether `pulls/{base}/{head}` returns closed PRs; fork-PR status location; real `default_paging_num`/`max_response_items`.
4. Run filter `event=pull_request` behaviour (sub-events) and `ref` normalisation for PR runs (`refs/pull/N/head`).
5. Release delete removes the git tag or not; `release create` on an existing tag behaviour.
6. Default pin/WIP prefixes accepted for draft PRs on this instance.
7. Minimum-major table (`minver.go`): job logs, run mutations, runner register, dispatch `return_run_info`.
8. Generic package upload path for the integration test (`/api/packages/...` is outside `/api/v1`).
9. Registry `server.json` schema URL, OCI label name and namespace casing; goreleaser Docker key name for the pinned version.
10. go-sdk v1.8.0: confirm `mcp.NewInMemoryTransports`, progress token access (`req.Params.GetProgressToken()`), that the raw `Server.AddTool` accepts our hand-built `InputSchema` (type object) and performs no argument validation, that `req.Extra.Header` carries request headers in HTTP mode, that `StreamableHTTPOptions` has `PropagateRequestCancellation` and `SessionTimeout`, and that returning only `Content` yields no `structuredContent`.
11. Timeline event type names `pull_scheduled_merge` / `pull_cancel_scheduled_merge` (10.1 rule 4) and the in-process Memo fallback.
12. Push-mirror minimum interval (`mirror.MinInterval`, default 10m) and whether `interval=0` plus `sync_on_commit=true` is accepted.
13. `.runner` JSON schema printed by `runner register` (UX snippet) against the pinned forgejo-runner; labels from `.runner` vs `config.yml`.
14. `os.Root` behaviour on Windows (junctions, case-insensitive roots) for the file-safety helper.
15. `.forgejo/workflows` vs `.gitea/workflows` precedence (`run workflows`), and that `.github/workflows` is never executed.
16. MinIO/S3 `ServeDirect` redirects for artifacts and attachments (Download follows them without Authorization).

## Appendix A. Review decisions

Three reviews were applied to this revision: engineering (E1-E24), agent-UX (U1-U17) and API-correctness against the v17.0.2 source (A1-A25, numbered in the order the review listed them). Every item was applied unless listed under "Not adopted or changed" below.

| ID | Issue | Resolution | Where |
|---|---|---|---|
| E1 | token sent to URLs from response bodies | external assets never downloaded; internal use path only, rebuilt on FORGEJO_URL | 5.7, 8.2 release, 8.5 attach |
| E2 | caches shared across pass-through tokens | token-hash namespaced caches, per-request redaction | 5.5, 2 `WithToken` |
| E3 | save_to/file under cwd | FORGEJO_SAVE_DIR only, cwd opt-in, `os.Root`, dot-components refused, HTTP off, `WORKDIR /data` | 3, 10.5, 13 |
| E4 | deny/toolsets bypass via forgejo_api | endpoint-based policy with canonical path and index match | 4 step 5 |
| E5 | read-only passes side-effect GET | GET denylist + `Action.ModeFor` | 2, 4 |
| E6 | bearer reused as Forgejo token | `X-Forgejo-Token`, Authorization only on loopback without bearer | 3 |
| E7 | go 1.24 vs SDK | `go 1.25.0`, CI on stable + min job | 0, 13 |
| E8 | gofumpt as linter | formatters only, `config verify` | 13 |
| E9 | dockers_v2 / Dockerfile / login | Dockerfile shape, qemu/buildx/login, platforms, no `latest` on prerelease | 13 |
| E10 | CRLF goldens, exe suffix, bash on Windows | `.gitattributes`, exeSuffix, integration package skips | 1, 12 |
| E11 | WPs sharing a package | split `repos`/`content`, `actions`/`ci`; `shared/ci` (WP6b) | 1, 14 |
| E12 | no clock/transport injection | `Config.HTTPClient`, `Clock`, `Env.Clock`, testutil helpers | 1, 2, 12.1 |
| E13 | harness helpers not frozen | WP0 skeleton, hand-written fixtures, WP19 re-record | 12.2, 14 |
| E14 | 120-600 s waits vs 60 s client timeouts | wait default 50 s, max 55 s; migrate/cron detached; README | 3, 10.1, 8.2, 8.11 |
| E15 | size caps dead above MAX_OUTPUT | rule (b): explicit `max_chars` raises the cap to 100000; trailers survive | 6.6 |
| E16 | SDK validation hides our errors | raw `Server.AddTool`, own validation, `map[string]any` free-form params | 0, 2 |
| E17 | destructiveHint default | explicit pointer values; test `additionalProperties == false` | 2, 12.1 |
| E18 | schema budget too small | measure first; initial 6,000 per tool / 60,000 total, frozen by WP4 | 12.1 |
| E19 | forgejo_api path/escape/upload | strip `/api/v1`, no double-escape, package upload moved to harness | 9.1, 12.2 |
| E20 | bootstrap TTY and runner dir perms | `-T`, `tr -d '\r'`, chmod 0777/0644 | 12.2 |
| E21 | token file BOM/UTF-16 | decode + reject non-printable | 3 |
| E22 | HTTP timeouts / cancellation | `ReadHeaderTimeout`, no `WriteTimeout`, `PropagateRequestCancellation`, `SessionTimeout` | 3 (names `[v]` 15.10) |
| E23 | server.json isRequired, README token, version | `isRequired:false`, token-file advice, BuildInfo fallback | 13, 1 |
| E24 | endpoints embed cycle, ownership | `endpoints/scope`, placeholder, swagger sha256, ownership table | 1, 9.5, 14 |
| U1 | ci_failure misses fail-fast run | no status filter; failed job counts | 10.4 |
| U2 | only newest failed run | group by workflow, report every failed group | 10.4 |
| U3 | caps cut trailers/log tails | trailer re-append, `CapTail`, `max_chars` everywhere | 6.6 |
| U4 | inconsistent default target | one rule, `target:` line | 3.1 step 4 |
| U5 | green before auto-merge lands | merged first; wait up to 30 s when scheduled; `mergeable:` line | 10.1, 10.3 |
| U6 | wait default vs client timeouts | same as E14; resume echoes the original selector | 10.1 |
| U7 | per-action params not discoverable | `[actions]` property prefixes, budget raised, issue `pos`/`subscribe_user` trimmed | 2, 7.1, 12.1 |
| U8 | runner register not completable | `labels` + printed `.runner` line and daemon command | 8.7, 12.2 |
| U9 | noisy 20/40-line tails | `matches:` scan, matrix dedupe, budget split | 10.4 |
| U10 | log tail of a failed job is cleanup | default `step=failed` for failed jobs | 8.6, 10.2 |
| U11 | four ways to get PR CI state | `pr.status` removed, `checks.get` without `pr` | 8.4, 8.6 |
| U12 | `#` overloaded | run number `no.56`; `#12` not accepted in run filters | 6.1, 6.9, 8.6 |
| U13 | vocabulary collisions | `first`, `which`, `all`, `include`, no generic `detail` | 7.1, 8.x |
| U14 | forgejo_endpoints needs 3 calls | compact param line for <= 5 results | 9.5 |
| U15 | no server instructions text | specified, generated, <= 700 chars | 4.1 |
| U16 | per-tool schema budget | same as E18 | 12.1 |
| U17 | header and review-state inconsistencies | one header form; review states = lowercase event enum | 2, 6.4, 6.9, 8.4 |
| A1 | lock body key | `{reason}`, default `resolved` | 8.5 issue |
| A2 | push mirror body | `interval` always sent; key mapping; ssh+credentials rejected | 8.2 |
| A3 | external asset upload | multipart with empty body | 8.2 release |
| A4 | `allow_manual_merge` pre-check | `manually-merged` not pre-checked | 10.3, 5.5 |
| A5 | hook secret on edit | rejected locally | 8.8 |
| A6 | timeline total | unknown-total form, `more` = raw page full | 6.7, 8.5 |
| A7 | search `private` | mapped to `is_private`; `private` never sent | 8.2 repo |
| A8 | PR milestone clear | via `PATCH /issues/{n}` | 8.4 |
| A9 | protect_set whitelist | `enable_push:true` added | 8.2 branch |
| A10 | `/` in rule/tag | `Seg()` for those, `Wild` only for wildcard routes | 8.2, 10.5 |
| A11 | body key names | spelled out for migrate, user rename, tokens | 8.2, 8.9, 8.11 |
| A12 | reviews pagination | `AllReviews` loop shared by `pr get` and `review list` | 8.4 |
| A13 | cross-host download redirects | `Client.Download` follows, drops Authorization | 5.2, 8.6 |
| A14 | non-informative 404 messages | `^[A-Za-z]+$` rule, show `errors[0]` | 11.2 |
| A15 | merge 409 variants | three new rows + 10.3 order | 10.3, 11.2 |
| A16 | `pr update` 422 literal | matched by status/literal | 11.2 |
| A17 | sha mismatch statuses | 400 on delete, 422 on patch | 8.3, 11.2 |
| A18 | commit list `+a -d` | removed from list lines (no extra diff cost) | 6.9, 8.3 |
| A19 | tag get lightweight | `git/tags` only when ids differ | 8.2 tag |
| A20 | workflow directories | `.forgejo` then `.gitea` on 404; `.github` skipped | 8.6 |
| A21 | issue link owner/repo | canonical `full_name`; Hint500 | 8.5, 11.2 |
| A22 | fork poll, head lookup | no poll; `pulls/{base}/{head}` | 8.2, 8.4 |
| A23 | combined status paging | `limit=max`, page on, client-side worst state | 8.4 |
| A24 | endpoints version | `-version` / `GET /version`, semver check, basePath ignored | 9.5 |
| A25 | comment delete no-op | GET first, detect still-present comment | 8.5 |

Not adopted or changed:
- E14: the review offered "default 50 s, raise by env"; adopted with max 55 s (not 600) so a mis-set client cannot hang; uploads stay in-call (a streamed upload cannot be resumed), migrate and cron_run are detached instead.
- E10: the review offered a `//go:build !windows` tag for the integration package; a runtime skip is used instead so the package still compiles and vets on Windows.
- E9: the alternative `--skip=docker` for the CI snapshot is not used; buildx/QEMU is set up so the same path as release is exercised.
- U7: the alternative "signature list in the tool description" is not used (descriptions stay <= 160 chars); only the property-prefix variant.
- U11: the alias variant of `pr.status` is not used; the action is removed.
- U12: the alternative `n56` is not used; `no.56` chosen. `#12` is rejected in run filters rather than kept as an alias.
- U14: the "always include required params" variant is not used; only the <= 5 results rule plus `detail=true`.
- U5: the optional `until=merged` param is not added (schema budget); the behaviour is automatic when an auto-merge is known to be scheduled.
- A6: `X-Total-Count` for the timeline is ignored entirely rather than used as a lower bound.
- A22: the review offered "skip the poll when the source is empty OR drop it"; dropped entirely.

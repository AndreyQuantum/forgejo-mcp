// Package integration runs the tools against a real Forgejo 17.0.2 (DESIGN.md
// section 12.2). The helpers drive the server through the real MCP stack
// (in-memory transport), so schemas, output and error texts are exercised.
//
// Environment:
//
//	FORGEJO_TEST_URL, FORGEJO_TEST_TOKEN   an existing instance and its admin token
//	FORGEJO_TEST_BOOTSTRAP=1               start a fresh container with bootstrap.sh
//	                                       (needs bash and docker; torn down after the run)
//
// Without any of them every test skips.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/gitremote"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/all"
)

// record switches the suite into fixture-recording mode:
// go test ./test/integration -run Record -record
var record = flag.Bool("record", false, "record API fixtures from the live instance")

// recordDir is where TestRecord writes sanitized API responses.
var recordDir = flag.String("record-dir", filepath.Join("testdata", "recorded"), "directory for -record output")

// callTimeout bounds one tool call; ci_wait may block up to FORGEJO_WAIT_MAX.
const callTimeout = 150 * time.Second

var (
	instURL      string // base URL without trailing slash, "" = no instance
	instToken    string
	bootstrapped bool
	bootDir      string
)

func TestMain(m *testing.M) {
	flag.Parse()
	instURL = strings.TrimRight(os.Getenv("FORGEJO_TEST_URL"), "/")
	instToken = os.Getenv("FORGEJO_TEST_TOKEN")
	if (instURL == "" || instToken == "") && os.Getenv("FORGEJO_TEST_BOOTSTRAP") == "1" {
		if err := bootstrap(); err != nil {
			fmt.Fprintln(os.Stderr, "integration: bootstrap failed:", err)
			teardown()
			os.Exit(1)
		}
	}
	code := m.Run()
	closeShared()
	if bootstrapped {
		teardown()
	}
	os.Exit(code)
}

// bootstrap runs bootstrap.sh and parses its single export line.
func bootstrap() error {
	if _, err := exec.LookPath("bash"); err != nil {
		return fmt.Errorf("bash not found: %w", err)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found: %w", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	bootDir = wd
	cmd := exec.Command("bash", "bootstrap.sh")
	cmd.Dir = wd
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	bootstrapped = true
	for _, f := range strings.Fields(string(out)) {
		switch {
		case strings.HasPrefix(f, "FORGEJO_TEST_URL="):
			instURL = strings.TrimRight(strings.Trim(strings.TrimPrefix(f, "FORGEJO_TEST_URL="), `'"`), "/")
		case strings.HasPrefix(f, "FORGEJO_TEST_TOKEN="):
			instToken = strings.Trim(strings.TrimPrefix(f, "FORGEJO_TEST_TOKEN="), `'"`)
		}
	}
	if instURL == "" || instToken == "" {
		return fmt.Errorf("bootstrap.sh printed no FORGEJO_TEST_URL/FORGEJO_TEST_TOKEN")
	}
	return nil
}

func teardown() {
	if bootDir == "" {
		return
	}
	cmd := exec.Command("bash", "teardown.sh")
	cmd.Dir = bootDir
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// skipUnlessIntegration skips the test unless FORGEJO_TEST_URL and
// FORGEJO_TEST_TOKEN are set (or the harness bootstrapped an instance).
// Every test starts with it.
func skipUnlessIntegration(t *testing.T) {
	t.Helper()
	if instURL == "" || instToken == "" {
		t.Skip("integration instance not configured: set FORGEJO_TEST_URL and FORGEJO_TEST_TOKEN, or FORGEJO_TEST_BOOTSTRAP=1 (needs bash and docker)")
	}
}

// ---- sessions -------------------------------------------------------------

// session is one in-process MCP server with a connected client.
type session struct {
	cs  *mcp.ClientSession
	ss  *mcp.ServerSession
	env *tools.Env
	reg *tools.Registry
}

// newSession builds a server against the test instance. extra overrides or
// adds FORGEJO_* variables (e.g. {"FORGEJO_READ_ONLY": "1"}); an empty value
// unsets the variable. The session is closed with t.Cleanup.
func newSession(t testing.TB, extra map[string]string) *session {
	t.Helper()
	s, err := openSession(extra)
	if err != nil {
		t.Fatalf("integration session: %v", err)
	}
	t.Cleanup(s.close)
	return s
}

func openSession(extra map[string]string) (*session, error) {
	vars := map[string]string{
		"FORGEJO_URL":      instURL,
		"FORGEJO_TOKEN":    instToken,
		"FORGEJO_WAIT_MAX": "120",
		"FORGEJO_TIMEOUT":  "60",
		"FORGEJO_SAVE_DIR": os.TempDir(),
	}
	for k, v := range extra {
		vars[k] = v
	}
	cfg, err := config.Load(nil, func(k string) string { return vars[k] })
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	client := forgejo.New(forgejo.Config{
		BaseURL:   cfg.URL,
		Token:     cfg.Token,
		Timeout:   cfg.Timeout,
		Sudo:      cfg.Sudo,
		UserAgent: "forgejo-mcp/integration",
		Clock:     clock.Real(),
		Debug:     cfg.Debug,
	})
	det := gitremote.NewDetector(gitremote.Options{
		InstanceURL: cfg.URL,
		SSHHosts:    cfg.SSHHosts,
		DefaultRepo: cfg.Repo,
		Dir:         cfg.Dir,
	})
	env := &tools.Env{Client: client, Cfg: cfg, Repo: det, Clock: clock.Real(), Memo: &tools.Memo{}}

	reg := tools.NewRegistry()
	all.RegisterAll(reg)
	if err := reg.Build(cfg); err != nil {
		return nil, fmt.Errorf("registry build: %w", err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "forgejo-mcp", Version: "integration"}, nil)
	if err := reg.Install(srv, env); err != nil {
		return nil, fmt.Errorf("registry install: %w", err)
	}
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		return nil, fmt.Errorf("server connect: %w", err)
	}
	cl := mcp.NewClient(&mcp.Implementation{Name: "integration-client", Version: "test"}, nil)
	cs, err := cl.Connect(ctx, ct, nil)
	if err != nil {
		_ = ss.Close()
		return nil, fmt.Errorf("client connect: %w", err)
	}
	return &session{cs: cs, ss: ss, env: env, reg: reg}, nil
}

func (s *session) close() {
	if s == nil {
		return
	}
	_ = s.cs.Close()
	_ = s.ss.Close()
}

// call invokes tool and returns the text of all text blocks and IsError. A
// protocol-level failure (unknown tool, schema error) is reported as an error
// text, like testutil.CallTool.
func (s *session) call(t testing.TB, tool string, args map[string]any) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	res, err := s.cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return "error: " + err.Error(), true
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// toolNames lists the tools the session exposes (tools/list).
func (s *session) toolNames(t testing.TB) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var names []string
	for tool, err := range s.cs.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		names = append(names, tool.Name)
	}
	return names
}

var (
	sharedMu  sync.Mutex
	shared    *session
	sharedErr error
)

// sharedSession returns the process-wide default session (admin token,
// FORGEJO_WAIT_MAX=120), created on first use.
func sharedSession(t testing.TB) *session {
	t.Helper()
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared == nil && sharedErr == nil {
		shared, sharedErr = openSession(nil)
	}
	if sharedErr != nil {
		t.Fatalf("integration session: %v", sharedErr)
	}
	return shared
}

func closeShared() {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	shared.close()
	shared = nil
}

// call invokes tool through the real MCP stack (in-memory transport) with
// args and returns the text and the IsError flag.
func call(t *testing.T, tool string, args map[string]any) (text string, isErr bool) {
	t.Helper()
	skipUnlessIntegration(t)
	return sharedSession(t).call(t, tool, args)
}

// mustOK calls tool and fails the test on IsError, returning the text.
func mustOK(t *testing.T, tool string, args map[string]any) string {
	t.Helper()
	text, isErr := call(t, tool, args)
	if isErr {
		t.Fatalf("%s %s failed: %s", tool, argSummary(args), text)
	}
	return text
}

// mustErr calls tool and fails the test unless IsError is set, returning the
// error text.
func mustErr(t *testing.T, tool string, args map[string]any) string {
	t.Helper()
	text, isErr := call(t, tool, args)
	if !isErr {
		t.Fatalf("%s %s: expected an error, got:\n%s", tool, argSummary(args), text)
	}
	return text
}

func argSummary(args map[string]any) string {
	b, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprint(args)
	}
	return string(b)
}

// ---- golden ---------------------------------------------------------------

var (
	reTime  = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:\d{2})?`)
	reSHA   = regexp.MustCompile(`\b[0-9a-f]{40}\b`)
	reShort = regexp.MustCompile(`\b[0-9a-f]{7,12}\b`)
	reRand  = regexp.MustCompile(`\bit-[a-z0-9]*[0-9][a-z0-9]{5,}\b`)
	reUUID  = regexp.MustCompile(`\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	reLocal = regexp.MustCompile(`https?://(?:localhost|127.0.0.1|[::1]|forgejo)(?::d+)?`)
)

// stabilize replaces the parts of a tool output that change between runs:
// the instance host, timestamps, commit ids, UUIDs and random name suffixes.
func stabilize(got string) string {
	if instURL != "" {
		got = strings.ReplaceAll(got, instURL, "https://forgejo.example.com")
	}
	got = reLocal.ReplaceAllString(got, "https://forgejo.example.com")
	got = reTime.ReplaceAllString(got, "<time>")
	got = reUUID.ReplaceAllString(got, "<uuid>")
	got = reSHA.ReplaceAllString(got, "<sha>")
	got = reShort.ReplaceAllStringFunc(got, func(s string) string {
		// Only hex words that mix digits and letters look like abbreviated SHAs.
		if strings.ContainsAny(s, "0123456789") && strings.ContainsAny(s, "abcdef") {
			return "<sha>"
		}
		return s
	})
	got = reRand.ReplaceAllString(got, "it-<rand>")
	return got
}

// golden compares the stable parts of got with testdata/<name>.golden
// (rewritten with -update).
func golden(t *testing.T, name, got string) {
	t.Helper()
	testutil.Golden(t, name, stabilize(got))
}

// uniqueName returns prefix + random suffix, e.g. it-3fa9c1 for prefix "it".
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%06x", prefix, rand.IntN(1<<24))
}

// ---- raw API access (setup, probes, recording) ------------------------------

var httpClient = &http.Client{Timeout: 60 * time.Second}

// rawAPI performs an authenticated request against /api/v1 and returns the
// status and body. The token never appears in errors.
func rawAPI(t testing.TB, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, instURL+"/api/v1"+path, rd)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "token "+instToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: transport error", method, path)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// requireAdmin skips the test when the token lacks the admin scope.
func requireAdmin(t *testing.T) {
	t.Helper()
	skipUnlessIntegration(t)
	if st, _ := rawAPI(t, http.MethodGet, "/admin/users?limit=1", nil); st != http.StatusOK {
		t.Skipf("token lacks the admin scope (GET /admin/users -> %d)", st)
	}
}

// requireRunner skips the test unless the runner e2e is enabled and docker exists.
func requireRunner(t *testing.T) {
	t.Helper()
	skipUnlessIntegration(t)
	if os.Getenv("FORGEJO_TEST_RUNNER") != "1" {
		t.Skip("runner e2e disabled: set FORGEJO_TEST_RUNNER=1")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not found")
	}
}

// runnerImage returns the runner image: FORGEJO_TEST_RUNNER_IMAGE or the pin
// in runner.version.
func runnerImage(t testing.TB) string {
	t.Helper()
	if v := strings.TrimSpace(os.Getenv("FORGEJO_TEST_RUNNER_IMAGE")); v != "" {
		return v
	}
	b, err := os.ReadFile("runner.version")
	if err != nil {
		t.Fatalf("runner.version: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// ---- record mode ------------------------------------------------------------

var (
	reEmail   = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	reTokenKV = regexp.MustCompile(`("(?:token|sha1|password|secret|reg_token|registration_token|token_last_eight)"\s*:\s*)"[^"]*"`)
)

// sanitizeFixture strips tokens, emails and the instance host from a recorded
// response so it can be committed.
func sanitizeFixture(b []byte) []byte {
	s := string(b)
	if instURL != "" {
		s = strings.ReplaceAll(s, instURL, "https://forgejo.example.com")
	}
	s = reLocal.ReplaceAllString(s, "https://forgejo.example.com")
	if instToken != "" {
		s = strings.ReplaceAll(s, instToken, "REDACTED")
	}
	s = reTokenKV.ReplaceAllString(s, `$1"REDACTED"`)
	s = reEmail.ReplaceAllStringFunc(s, func(e string) string {
		if strings.HasSuffix(e, "@example.com") || strings.HasSuffix(e, "@example.invalid") {
			return e
		}
		return "user@example.com"
	})
	return []byte(s)
}

// recordFixture GETs path (relative to /api/v1) and stores the sanitized body
// as <record-dir>/<name>.json. Area WPs call it from their own Record tests.
func recordFixture(t *testing.T, name, path string) {
	t.Helper()
	if !*record {
		return
	}
	st, body := rawAPI(t, http.MethodGet, path, nil)
	if st != http.StatusOK {
		t.Fatalf("record %s: GET %s -> %d", name, path, st)
	}
	if err := os.MkdirAll(*recordDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	out := sanitizeFixture(body)
	if json.Indent(&pretty, out, "", "  ") == nil {
		out = pretty.Bytes()
	}
	if err := os.WriteFile(filepath.Join(*recordDir, name+".json"), append(bytes.TrimRight(out, "\n"), '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRecord is the entry point of fixture recording (go test -run Record -record).
// It records the instance-level responses every WP needs; area files add
// their own recordings next to their tests.
func TestRecord(t *testing.T) {
	skipUnlessIntegration(t)
	if !*record {
		t.Skip("pass -record to record fixtures")
	}
	recordFixture(t, "version", "/version")
	recordFixture(t, "settings_api", "/settings/api")
	recordFixture(t, "settings_repository", "/settings/repository")
	recordFixture(t, "user", "/user")
}

// ---- harness self-tests -------------------------------------------------------

func TestHarnessSession(t *testing.T) {
	skipUnlessIntegration(t)
	s := sharedSession(t)
	t.Logf("tools/list: %d tools", len(s.toolNames(t)))
	if names := s.toolNames(t); len(names) > 0 {
		if _, isErr := s.call(t, names[0], map[string]any{"action": "help"}); isErr {
			t.Logf("help on %s returned an error (tool may be single-purpose)", names[0])
		}
	}
}

func TestHarnessStabilize(t *testing.T) {
	got := stabilize("repo it-3fa9c1 sha 0123456789abcdef0123456789abcdef01234567 at 2026-11-22T10:00:00Z c0ffee1")
	want := "repo it-<rand> sha <sha> at <time> <sha>"
	if got != want {
		t.Fatalf("stabilize:\n got %q\nwant %q", got, want)
	}
}

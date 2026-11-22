package repos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// repoPath returns /repos/{owner}/{repo} plus extra path parts (each part is
// escaped; wrap multi-segment parts with forgejo.Wild).
func repoPath(c *tools.Call, parts ...string) string {
	return forgejo.Path(append([]string{"repos", c.Owner, c.Repo}, parts...)...)
}

func fullName(c *tools.Call) string { return c.Owner + "/" + c.Repo }

// limitOf returns the effective page size: the param, else FORGEJO_DEFAULT_LIMIT.
// The client clamps it to the server maximum.
func limitOf(c *tools.Call, n int) int {
	if n > 0 {
		return min(n, 100)
	}
	if c.Env.Cfg != nil && c.Env.Cfg.DefaultLimit > 0 {
		return c.Env.Cfg.DefaultLimit
	}
	return 20
}

// window cuts a client-paged slice (lists the server does not paginate).
func window[T any](items []T, page, limit int) []T {
	if page < 1 {
		page = 1
	}
	start := (page - 1) * limit
	if start >= len(items) {
		return nil
	}
	return items[start:min(start+limit, len(items))]
}

func text(s string) *tools.Result { return &tools.Result{Text: s} }

func okf(format string, a ...any) *tools.Result {
	return &tools.Result{Text: "ok: " + fmt.Sprintf(format, a...) + "\n"}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// checkKeys rejects keys of set that are not in valid.
func checkKeys(c *tools.Call, what string, set map[string]any, valid []string) *tools.Result {
	ok := make(map[string]bool, len(valid))
	for _, k := range valid {
		ok[k] = true
	}
	var bad []string
	for _, k := range sortedKeys(set) {
		if !ok[k] {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return c.Fail("invalid: unknown %s key %s; valid: %s", what, strings.Join(bad, ","), strings.Join(valid, ","))
}

func apiStatus(err error) int {
	var ae *forgejo.APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// addHint sets the hint of an APIError with the given status.
func addHint(err error, status int, hint string) error {
	var ae *forgejo.APIError
	if errors.As(err, &ae) && ae.Status == status && ae.Hint == "" {
		ae.Hint = hint
	}
	return err
}

// replaceHint overrides the hint of an APIError with the given status (the
// generic hint of that status is misleading for this action).
func replaceHint(err error, status int, hint string) error {
	var ae *forgejo.APIError
	if errors.As(err, &ae) && ae.Status == status {
		ae.Hint = hint
	}
	return err
}

// send performs a JSON request through Client.Do and decodes the answer into
// out (nil allowed). timeout 0 uses the client default.
func send(ctx context.Context, cl *forgejo.Client, method, path string, q url.Values, in, out any, timeout time.Duration) (*forgejo.Response, error) {
	resp, err := cl.Do(ctx, forgejo.Request{Method: method, Path: path, Query: q, JSON: in, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	if out != nil && resp.Status != http.StatusNoContent && len(bytes.TrimSpace(resp.Body)) > 0 {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return resp, &forgejo.APIError{Message: "invalid JSON in response: " + err.Error(), Method: method, Path: path}
		}
	}
	return resp, nil
}

// pageOf converts the (page, limit) params for list calls.
func pageOf(n int) int { return max(n, 1) }

func itoa(n int) string { return strconv.Itoa(n) }

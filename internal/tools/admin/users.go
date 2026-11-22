package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

var visibilities = map[string]bool{"": true, "public": true, "limited": true, "private": true}

var userSorts = map[string]bool{"oldest": true, "newest": true, "alphabetically": true, "reversealphabetically": true, "recentupdate": true, "leastupdate": true}

func runUsersList(c *tools.Call, p *adminParams) (*tools.Result, error) {
	q := url.Values{}
	if p.SourceID != nil {
		q.Set("source_id", strconv.FormatInt(*p.SourceID, 10))
	}
	if p.LoginName != "" {
		q.Set("login_name", p.LoginName)
	}
	if p.Sort != "" {
		if !userSorts[p.Sort] {
			return invalid(c, "sort %q; valid: oldest,newest,alphabetically,reversealphabetically,recentupdate,leastupdate", p.Sort), nil
		}
		q.Set("sort", p.Sort)
	}
	return listLines(c, "users", "/admin/users", q, p, userLine)
}

func runUsersCreate(c *tools.Call, p *adminParams) (*tools.Result, error) {
	if !okName(p.User) {
		return invalid(c, "user"), nil
	}
	if !visibilities[p.Visibility] {
		return invalid(c, "visibility %q; valid: public,limited,private", p.Visibility), nil
	}
	external := p.SourceID != nil && *p.SourceID != 0
	if p.Password == "" && !external {
		return invalid(c, "password is required for a local user (or pass source_id of an external auth source)"), nil
	}
	body := map[string]any{"username": p.User, "email": p.Email}
	if p.Password != "" {
		body["password"] = p.Password
	}
	if p.FullName != "" {
		body["full_name"] = p.FullName
	}
	if external {
		body["login_name"] = p.LoginName
		body["source_id"] = *p.SourceID
	}
	if p.MustChangePassword != nil {
		body["must_change_password"] = *p.MustChangePassword
	}
	if p.Restricted != nil {
		body["restricted"] = *p.Restricted
	}
	if p.Visibility != "" {
		body["visibility"] = p.Visibility
	}
	var created user
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, "/admin/users", nil, body, &created); err != nil {
		return nil, err
	}
	// The create form has no admin / active / prohibit_login: apply them with
	// a follow-up edit.
	var followed []string
	follow := map[string]any{}
	for _, f := range []struct {
		key string
		val *bool
	}{{"admin", p.Admin}, {"active", p.Active}, {"prohibit_login", p.ProhibitLogin}} {
		if f.val != nil {
			follow[f.key] = *f.val
			followed = append(followed, f.key)
		}
	}
	if len(follow) > 0 {
		if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPatch, forgejo.Path("admin", "users", p.User), nil, follow, nil); err != nil {
			return &tools.Result{Text: fmt.Sprintf("error: created user %s (id %d) but setting %s failed: %v",
				created.Login, created.ID, strings.Join(followed, ","), err), IsError: true}, nil
		}
	}
	text := fmt.Sprintf("ok: created user %s (id %d)", p.User, created.ID)
	if len(followed) > 0 {
		text += " with " + strings.Join(followed, ",")
	}
	return &tools.Result{Text: text}, nil
}

func runUsersEdit(c *tools.Call, p *adminParams) (*tools.Result, error) {
	if !okName(p.User) {
		return invalid(c, "user"), nil
	}
	if !visibilities[p.Visibility] {
		return invalid(c, "visibility %q; valid: public,limited,private", p.Visibility), nil
	}
	body := map[string]any{}
	var changed []string
	set := func(key string, v any) {
		body[key] = v
		changed = append(changed, key)
	}
	if p.Email != "" {
		set("email", p.Email)
	}
	if p.FullName != "" {
		set("full_name", p.FullName)
	}
	if p.Password != "" {
		set("password", p.Password)
	}
	if p.Visibility != "" {
		set("visibility", p.Visibility)
	}
	for _, f := range []struct {
		key string
		val *bool
	}{
		{"must_change_password", p.MustChangePassword},
		{"admin", p.Admin},
		{"active", p.Active},
		{"prohibit_login", p.ProhibitLogin},
		{"restricted", p.Restricted},
	} {
		if f.val != nil {
			set(f.key, *f.val)
		}
	}
	// The server requires login_name and source_id together: fill the missing
	// half from the current user record.
	if p.LoginName != "" || p.SourceID != nil {
		login, src := p.LoginName, p.SourceID
		if login == "" || src == nil {
			var cur user
			if _, err := c.Env.Client.GetJSON(c.Ctx, forgejo.Path("users", p.User), nil, &cur); err != nil {
				return nil, err
			}
			if login == "" {
				login = cur.LoginName
			}
			if src == nil {
				id := cur.SourceID
				src = &id
			}
		}
		set("login_name", login)
		body["source_id"] = *src
		changed = append(changed, "source_id")
	}
	if len(body) == 0 {
		return invalid(c, "nothing to change; pass at least one of email, password, full_name, login_name, source_id, must_change_password, admin, active, prohibit_login, restricted, visibility"), nil
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPatch, forgejo.Path("admin", "users", p.User), nil, body, nil); err != nil {
		return nil, err
	}
	// Report names only: never the password value.
	return &tools.Result{Text: fmt.Sprintf("ok: updated user %s: %s", p.User, strings.Join(changed, ","))}, nil
}

func runUsersDelete(c *tools.Call, p *adminParams) (*tools.Result, error) {
	if !okName(p.User) {
		return invalid(c, "user"), nil
	}
	if r := c.Confirm(p.Confirm, p.User); r != nil {
		return r, nil
	}
	q := url.Values{}
	if p.Purge {
		q.Set("purge", "true")
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: forgejo.Path("admin", "users", p.User), Query: q}); err != nil {
		return nil, err
	}
	text := "ok: deleted user " + p.User
	if p.Purge {
		text += " (purged)"
	}
	return &tools.Result{Text: text}, nil
}

func runUsersRename(c *tools.Call, p *adminParams) (*tools.Result, error) {
	if !okName(p.User) || !okName(p.NewName) {
		return invalid(c, "user and new_name"), nil
	}
	// The admin rename body key is new_username (the org rename key is new_name).
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, forgejo.Path("admin", "users", p.User, "rename"), nil,
		map[string]any{"new_username": p.NewName}, nil); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: renamed user %s -> %s", p.User, p.NewName)}, nil
}

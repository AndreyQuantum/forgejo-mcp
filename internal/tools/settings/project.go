package settings

import "fmt"

// Projection structs (DESIGN.md 6.9): only the JSON paths the tools use, so
// raw bodies are never held longer than needed.

// hookObj is a webhook of any scope.
type hookObj struct {
	ID                  int64          `json:"id"`
	Type                string         `json:"type"`
	Active              bool           `json:"active"`
	Events              []string       `json:"events"`
	BranchFilter        string         `json:"branch_filter"`
	AuthorizationHeader string         `json:"authorization_header"`
	Config              map[string]any `json:"config"`
	URL                 string         `json:"url"`
	ContentType         string         `json:"content_type"`
	CreatedAt           string         `json:"created_at"`
	UpdatedAt           string         `json:"updated_at"`
}

func (h hookObj) cfg(key string) string {
	if v, ok := h.Config[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

// hookURL is config.url, falling back to the top-level url field.
func (h hookObj) hookURL() string {
	if u := h.cfg("url"); u != "" {
		return u
	}
	return h.URL
}

// hookContentType is config.content_type, falling back to the top-level field.
func (h hookObj) hookContentType() string {
	if v := h.cfg("content_type"); v != "" {
		return v
	}
	return h.ContentType
}

// deployKey is a repository deploy key.
type deployKey struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Fingerprint string `json:"fingerprint"`
	ReadOnly    bool   `json:"read_only"`
	CreatedAt   string `json:"created_at"`
}

// collaborator is an entry of the collaborators list.
type collaborator struct {
	Login string `json:"login"`
}

// collabPermission is GET R/collaborators/{user}/permission.
type collabPermission struct {
	Permission string `json:"permission"`
	RoleName   string `json:"role_name"`
}

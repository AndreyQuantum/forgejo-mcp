package forgejo

import (
	"fmt"
	"net/http"
	"strings"
)

// APIError is a non-2xx answer (or a transport failure mapped to a status).
type APIError struct {
	Status       int
	Message      string
	Method, Path string
	Body         string
	Hint         string
}

// Error returns the compact one-liner of section 11.1. WP2 implements the
// full mapping table of 11.2.
func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "error: %d %s", e.Status, strings.ToLower(http.StatusText(e.Status)))
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if e.Hint != "" {
		b.WriteString("\nhint: " + e.Hint)
	}
	return b.String()
}

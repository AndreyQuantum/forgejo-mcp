// Package forgejo is the HTTP client and API-level helpers (DESIGN.md
// sections 2, 5, 11). WP0 defines the contract; WP2 fills the bodies.
package forgejo

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
)

// Config configures a Client.
type Config struct {
	BaseURL     string
	Token       string
	Timeout     time.Duration
	Sudo        string
	CAFile      string
	InsecureTLS bool
	UserAgent   string
	HTTPClient  *http.Client // tests inject a Transport
	Clock       clock.Clock  // nil = real; retries/backoff sleep through it
}

// Client talks to one Forgejo instance.
type Client struct {
	cfg   Config
	cache *Cache
}

// New returns a Client.
func New(cfg Config) *Client { return &Client{cfg: cfg, cache: NewCache()} }

// WithToken returns a client that uses tok for this request chain. It shares
// the transport and the `version` and `settings/api` caches only; every other
// cache is namespaced by sha256(tok)[:8] and tok joins the redaction list.
func (c *Client) WithToken(tok string) *Client { panic("WP2") }

// Request describes one API call.
type Request struct {
	Method, Path string // Path relative to /api/v1, ALREADY escaped (use Path()/Seg())
	Query        url.Values
	JSON         any       // marshalled, sets Content-Type: application/json
	Body         io.Reader // raw body (file upload); with ContentType
	ContentType  string
	Header       http.Header   // extra, e.g. Range
	Timeout      time.Duration // override (default cfg.Timeout)
	MaxBody      int64         // default 8 MiB; excess is dropped and Response.Truncated=true
	API          string        // "" = /api/v1, "forgejo" = /api/forgejo/v1, "root" = no prefix
}

// Response is a successful (2xx) answer.
type Response struct {
	Status    int
	Header    http.Header
	Body      []byte
	Truncated bool
	Total     int64 // X-Total-Count, or -1 when absent
}

// Do performs the request. 2xx returns a nil error; anything else *APIError.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) { panic("WP2") }

// Download streams a GET body into w (no MaxBody). It follows up to 3
// redirects including cross-host ones and drops Authorization and Sudo on
// every hop whose host differs from the instance host.
func (c *Client) Download(ctx context.Context, r Request, w io.Writer) (n int64, resp *Response, err error) {
	panic("WP2")
}

// GetJSON performs a GET and decodes the JSON body into out.
func (c *Client) GetJSON(ctx context.Context, path string, q url.Values, out any) (*Response, error) {
	panic("WP2")
}

// SendJSON sends in as JSON; out may be nil; 204/empty bodies are fine.
func (c *Client) SendJSON(ctx context.Context, method, path string, q url.Values, in, out any) (*Response, error) {
	panic("WP2")
}

// ServerVersion returns the parsed /version, cached for the process lifetime.
func (c *Client) ServerVersion(ctx context.Context) (Version, error) { panic("WP2") }

// Limits returns the default and max page size from /settings/api (cached;
// falls back to 30/50).
func (c *Client) Limits(ctx context.Context) (def, maxItems int) { panic("WP2") }

// Me returns the authenticated user (cached 10 min).
func (c *Client) Me(ctx context.Context) (*User, error) { panic("WP2") }

// List fetches one page into out, sets page/limit and clamps limit to the
// server maximum.
func (c *Client) List(ctx context.Context, path string, q url.Values, page, limit int, out any) (Page, error) {
	panic("WP2")
}

// Cache returns the client's TTL cache.
func (c *Client) Cache() *Cache { return c.cache }

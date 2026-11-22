package forgejo

import "github.com/AndreyQuantum/forgejo-mcp/internal/format"

// Page describes one page of a list. It is an alias of format.Page so that
// format.ListHeader can take it without format importing this package.
type Page = format.Page

// PageFrom computes Page from a response. shown is the RAW item count before
// any client-side filtering. More = page*limit < Total, else shown==limit
// (also the rule for lists with an unreliable total, section 6.7).
func PageFrom(resp *Response, page, limit, shown int) Page { panic("WP2") }

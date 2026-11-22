package forgejo

import "github.com/AndreyQuantum/forgejo-mcp/internal/format"

// Page describes one page of a list. It is an alias of format.Page so that
// format.ListHeader can take it without format importing this package.
type Page = format.Page

// PageFrom computes Page from a response. shown is the RAW item count before
// any client-side filtering. More = page*limit < Total, else shown==limit
// (also the rule for lists with an unreliable total, section 6.7).
func PageFrom(resp *Response, page, limit, shown int) Page {
	total := int64(-1)
	if resp != nil {
		total = resp.Total
	}
	p := Page{Page: page, Limit: limit, Total: total, Shown: shown}
	if total >= 0 {
		p.More = int64(page)*int64(limit) < total
	} else {
		p.More = limit > 0 && shown == limit
	}
	return p
}

// PageUnknownTotal builds the Page of a list whose total is unreliable
// (timeline, server-side post-filtered lists, section 6.7): More is true when
// the RAW page was full.
func PageUnknownTotal(page, limit, rawShown, shown int) Page {
	return Page{Page: page, Limit: limit, Total: -1, Shown: shown, More: limit > 0 && rawShown == limit}
}

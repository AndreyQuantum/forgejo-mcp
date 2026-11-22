package issues

import "github.com/AndreyQuantum/forgejo-mcp/internal/tools"

// Register adds every tool of this area to r.
func Register(r *tools.Registry) {
	registerIssue(r)
	registerComment(r)
	registerLabel(r)
	registerMilestone(r)
	registerAttach(r)
}

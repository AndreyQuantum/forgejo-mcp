package users

import "github.com/AndreyQuantum/forgejo-mcp/internal/tools"

// Register adds every tool of this area to r.
func Register(r *tools.Registry) {
	registerUser(r)
	registerOrg(r)
	registerNotify(r)
}

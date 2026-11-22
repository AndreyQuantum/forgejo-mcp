// Package all registers every tool area.
package all

import (
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/actions"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/admin"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/ci"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/content"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/issues"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/meta"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/packages"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/pulls"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/repos"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/runners"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/settings"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/users"
)

// RegisterAll adds every area to r.
func RegisterAll(r *tools.Registry) {
	meta.Register(r)
	repos.Register(r)
	content.Register(r)
	pulls.Register(r)
	issues.Register(r)
	actions.Register(r)
	ci.Register(r)
	runners.Register(r)
	settings.Register(r)
	users.Register(r)
	packages.Register(r)
	admin.Register(r)
}

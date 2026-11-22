package tools

import (
	"strconv"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
)

// minMajorTable holds per-action minimum Forgejo majors (section 5.4). The
// values are `[v]` items: adjust after the live integration run (WP19).
var minMajorTable = map[string]int{
	"run.list":        16,
	"run.job":         16,
	"run.log":         16,
	"run.cancel":      16,
	"run.rerun":       16,
	"run.rerun_job":   16,
	"run.delete":      16,
	"runner.register": 15,
}

// MinMajorFor returns the effective minimum major for tool.action: the larger
// of the table value and the value the Action itself declares.
func MinMajorFor(tool, action string, declared int) int {
	if v := minMajorTable[tool+"."+action]; v > declared {
		return v
	}
	return declared
}

// CheckMinVersion returns the error text of section 5.4
// (`<tool>.<action> needs Forgejo >= N (server is X)`) or "" when satisfied.
func CheckMinVersion(tool, action string, declared int, v forgejo.Version) string {
	need := MinMajorFor(tool, action, declared)
	if need == 0 || v.Atleast(need) {
		return ""
	}
	return tool + "." + action + " needs Forgejo >= " + strconv.Itoa(need) + " (server is " + v.String() + ")"
}

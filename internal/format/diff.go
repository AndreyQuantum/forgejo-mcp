package format

// FileDiff is one file section of a unified diff.
type FileDiff struct {
	Path    string
	OldPath string
	Status  string // A, M, D, R, C
	Binary  bool
	Added   int
	Removed int
	Text    string // the raw section including headers
}

// SplitDiff splits a unified diff into per-file sections (section 8.4).
func SplitDiff(diff string) []FileDiff { panic("WP3") }

// FilterDiff keeps the files matching any of the path globs.
func FilterDiff(files []FileDiff, globs []string) []FileDiff { panic("WP3") }

// RenderDiff concatenates files within max chars and adds the omitted-files
// trailer (outside the budget) when something was cut.
func RenderDiff(files []FileDiff, max int) string { panic("WP3") }

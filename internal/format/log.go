package format

// LogLine is one cleaned log line with its 1-based line number in the
// fetched range.
type LogLine struct {
	N    int
	Text string
}

// CleanLog applies the cleaning steps of section 10.2 step 4: strip the
// leading timestamp and ANSI sequences, keep the text after the last '\r',
// turn ::group:: markers into "> title" headers, drop empty lines and collapse
// runs of identical lines into "line (x7)". When partialFirst is true the
// first line is dropped (the range started inside a line).
func CleanLog(raw string, partialFirst bool) []LogLine { panic("WP3") }

// TailLines returns the last n lines (all when n <= 0).
func TailLines(lines []LogLine, n int) []LogLine { panic("WP3") }

// ErrorMatches scans lines case-insensitively for error markers
// (error|fail|panic|exception|fatal|assert) and returns at most max matches,
// skipping lines that only repeat the exit summary (section 10.4).
func ErrorMatches(lines []LogLine, max int) []LogLine { panic("WP3") }

package format

import (
	"fmt"
	"regexp"
	"strings"
)

// LogLine is one cleaned log line with its 1-based line number in the
// fetched range.
type LogLine struct {
	N    int
	Text string
}

var (
	logTimestamp = regexp.MustCompile(`^\d{4}-\d\d-\d\dT[\d:.]+Z `)
	logANSI      = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")
	logExit      = regexp.MustCompile(`(?i)^(?:error: )?(?:exit status \d+|exit code:? ?\d+|process completed with exit code \d+\.?)$`)
	logError     = regexp.MustCompile(`(?i)error|fail|panic|exception|fatal|assert`)
)

// CleanLog applies the cleaning steps of section 10.2 step 4: strip the
// leading timestamp and ANSI sequences, keep the text after the last '\r',
// turn ::group:: markers into "> title" headers, drop empty lines and collapse
// runs of identical lines into "line (x7)". When partialFirst is true the
// first raw line is dropped (the range started inside a line). N is the
// 1-based index of the raw line in the fetched range (the dropped partial
// line counts, so numbers stay aligned with the range); a collapsed run
// carries the number of its first line.
func CleanLog(raw string, partialFirst bool) []LogLine {
	rawLines := strings.Split(raw, "\n")
	var out []LogLine
	run := 0 // repeat count of the last emitted line
	flush := func() {
		if run > 1 {
			out[len(out)-1].Text += fmt.Sprintf(" (x%d)", run)
		}
		run = 0
	}
	for i, l := range rawLines {
		if i == 0 && partialFirst {
			continue
		}
		l = strings.TrimRight(l, "\r")
		if j := strings.LastIndexByte(l, '\r'); j >= 0 {
			l = l[j+1:]
		}
		l = logTimestamp.ReplaceAllString(l, "")
		l = logANSI.ReplaceAllString(l, "")
		l = strings.TrimRight(l, " \t")
		if strings.HasPrefix(l, "::endgroup::") || strings.HasPrefix(l, "##[endgroup]") {
			continue
		}
		if t, ok := strings.CutPrefix(l, "::group::"); ok {
			l = groupHeader(t)
		} else if t, ok := strings.CutPrefix(l, "##[group]"); ok {
			l = groupHeader(t)
		}
		if strings.TrimSpace(l) == "" {
			continue
		}
		if len(out) > 0 && run > 0 && out[len(out)-1].Text == l {
			run++
			continue
		}
		flush()
		out = append(out, LogLine{N: i + 1, Text: l})
		run = 1
	}
	flush()
	return out
}

func groupHeader(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	return "> " + title
}

// TailLines returns the last n lines (all when n <= 0).
func TailLines(lines []LogLine, n int) []LogLine {
	if n <= 0 || n >= len(lines) {
		return lines
	}
	return lines[len(lines)-n:]
}

// ErrorMatches scans lines case-insensitively for error markers
// (error|fail|panic|exception|fatal|assert) and returns at most max matches
// (the first ones), skipping lines that only repeat the exit summary
// (section 10.4).
func ErrorMatches(lines []LogLine, max int) []LogLine {
	var out []LogLine
	for _, l := range lines {
		if max > 0 && len(out) >= max {
			break
		}
		if !logError.MatchString(l.Text) {
			continue
		}
		t := strings.TrimSpace(l.Text)
		if i := strings.LastIndex(t, " (x"); i > 0 && strings.HasSuffix(t, ")") {
			t = t[:i] // ignore a dedupe suffix when judging the exit summary
		}
		if logExit.MatchString(t) {
			continue
		}
		out = append(out, l)
	}
	return out
}

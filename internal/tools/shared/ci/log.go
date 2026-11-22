package ci

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
)

// LogOpts selects what FetchJobLog reads and how it is cut (section 10.2).
type LogOpts struct {
	Owner, Repo string
	Job         int64
	Run         int64  // with Name: select the job by name
	Name        string // job name (exact, then case-insensitive prefix)
	Step        string // "", "failed" or a number
	Attempt     int
	Tail        int // default 80
	First       int
	Grep        string
	IgnoreCase  bool
	Offset      *int64
	MaxChars    int // body budget, already clamped by Call.Chars

	// Known is an already fetched job (with steps) so FetchJobLog does not
	// read it again; its ID is used when Job is 0.
	Known *Job
}

// JobLog is the result of FetchJobLog.
type JobLog struct {
	Header     string   // `job 5678 "build" attempt 1 step 3 "Run tests" [failure] | ...`
	Text       string   // cleaned, capped text (without the header); may end with a `[truncated: ...]` marker
	Lines      []string // cleaned lines of the fetched range (for digests)
	NextOffset int64
	NoLog      string // non-empty: `no log: <message>` (expired / not executed)

	// Numbered holds the cleaned lines of the fetched range with their
	// 1-based line number inside that range (digests scan it for markers).
	Numbered []format.LogLine
	// Job is the job the log belongs to (with steps).
	Job *Job
	// StepNumber is the selected step, -1 when the whole log was read.
	StepNumber int
	StepName   string
	// Total is the log size in bytes (the step slice when a step was
	// selected), -1 when unknown (grep mode).
	Total int64
}

// Render returns the header, the text and, when set, the `next_offset:` line.
func (l *JobLog) Render() string {
	if l.NoLog != "" {
		if l.Header != "" {
			return l.Header + "\n" + l.NoLog
		}
		return l.NoLog
	}
	s := l.Header
	if l.Text != "" {
		s += "\n" + l.Text
	}
	if l.NextOffset > 0 {
		s += "\nnext_offset: " + strconv.FormatInt(l.NextOffset, 10)
	}
	return s
}

const (
	defaultTail      = 80
	defaultLogChars  = 8000
	minLogBody       = 4 << 20
	maxGrepMatches   = 60
	grepNoContext    = "(no context available; use step/tail to read around)"
	logTruncatedHint = "pass tail=N, grep=, or step="
)

var reContentRange = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+|\*)$`)

// PickJob selects a job by name: exact, then case-insensitive, then
// case-insensitive prefix. Ambiguity and absence are errors listing candidates.
func PickJob(jobs []Job, name string) (Job, error) {
	match := func(f func(j Job) bool) []Job {
		var out []Job
		for _, j := range jobs {
			if f(j) {
				out = append(out, j)
			}
		}
		return out
	}
	lower := strings.ToLower(name)
	for _, f := range []func(j Job) bool{
		func(j Job) bool { return j.Name == name },
		func(j Job) bool { return strings.EqualFold(j.Name, name) },
		func(j Job) bool { return strings.HasPrefix(strings.ToLower(j.Name), lower) },
	} {
		m := match(f)
		switch len(m) {
		case 0:
			continue
		case 1:
			return m[0], nil
		default:
			parts := make([]string, len(m))
			for i, j := range m {
				parts[i] = fmt.Sprintf("%d %s", j.ID, j.Name)
			}
			return Job{}, fmt.Errorf("ambiguous job name %q; candidates: %s", name, strings.Join(parts, ", "))
		}
	}
	names := make([]string, len(jobs))
	for i, j := range jobs {
		names[i] = j.Name
	}
	return Job{}, fmt.Errorf("no job named %q; jobs: %s", name, strings.Join(names, ", "))
}

// SelectStep applies the step rules of section 10.2 step 2. spec is "", "failed"
// or a number. ok is false when the whole log is to be read.
func SelectStep(job *Job, spec string) (step Step, ok bool, err error) {
	if spec == "" && job.Status == "failure" {
		spec = "failed"
	}
	switch spec {
	case "":
		return Step{}, false, nil
	case "failed":
		for _, s := range job.Steps {
			if s.Status == "failure" {
				return s, true, nil
			}
		}
		for i := len(job.Steps) - 1; i >= 0; i-- {
			switch job.Steps[i].Status {
			case "", "waiting", "skipped", "unknown", "blocked":
				continue
			}
			return job.Steps[i], true, nil
		}
		return Step{}, false, nil
	}
	n, convErr := strconv.Atoi(spec)
	if convErr != nil || n < 0 {
		return Step{}, false, errors.New(`invalid: step must be a number or "failed"`)
	}
	for _, s := range job.Steps {
		if s.Number == n {
			return s, true, nil
		}
	}
	return Step{Number: n}, true, nil
}

func jobHeader(job *Job, step Step, hasStep bool) string {
	s := fmt.Sprintf("job %d %q", job.ID, job.Name)
	if job.Attempt > 0 {
		s += fmt.Sprintf(" attempt %d", job.Attempt)
	}
	if hasStep {
		s += fmt.Sprintf(" step %d", step.Number)
		if step.Name != "" {
			s += fmt.Sprintf(" %q", step.Name)
		}
	}
	if job.Status != "" {
		s += " [" + job.Status + "]"
	}
	return s
}

// FetchJobLog implements section 10.2.
func FetchJobLog(ctx context.Context, c *forgejo.Client, o LogOpts) (*JobLog, error) {
	if o.Tail <= 0 {
		o.Tail = defaultTail
	}
	if o.MaxChars <= 0 {
		o.MaxChars = defaultLogChars
	}
	job := o.Known
	jobID := o.Job
	if jobID == 0 && job != nil {
		jobID = job.ID
	}
	if jobID == 0 {
		if o.Run == 0 || o.Name == "" {
			return nil, errors.New("invalid: pass job, or run with name")
		}
		jobs, err := ListJobs(ctx, c, o.Owner, o.Repo, o.Run)
		if err != nil {
			return nil, err
		}
		picked, err := PickJob(jobs, o.Name)
		if err != nil {
			return nil, err
		}
		jobID = picked.ID
	}
	if job == nil || job.ID != jobID {
		var err error
		if job, err = GetJob(ctx, c, o.Owner, o.Repo, jobID); err != nil {
			return nil, err
		}
	}
	step, hasStep, err := SelectStep(job, o.Step)
	if err != nil {
		return nil, err
	}
	res := &JobLog{Job: job, StepNumber: -1, Total: -1, Header: jobHeader(job, step, hasStep)}
	if hasStep {
		res.StepNumber, res.StepName = step.Number, step.Name
	}

	q := url.Values{}
	if hasStep {
		q.Set("step", strconv.Itoa(step.Number))
	}
	if o.Attempt > 0 {
		q.Set("attempt", strconv.Itoa(o.Attempt))
	}
	path := forgejo.Path("repos", o.Owner, o.Repo, "actions", "jobs", strconv.FormatInt(jobID, 10), "logs")
	maxBody := int64(max(minLogBody, 2*o.MaxChars))

	get := func(query url.Values, rng string) (*forgejo.Response, bool, error) {
		req := forgejo.Request{Method: http.MethodGet, Path: path, Query: query, MaxBody: maxBody}
		if rng != "" {
			req.Header = http.Header{"Range": {rng}}
		}
		resp, err := c.Do(ctx, req)
		if err == nil {
			return resp, false, nil
		}
		var ae *forgejo.APIError
		if errors.As(err, &ae) {
			if msg, ok := ae.NoLog(); ok {
				res.NoLog = "no log: " + msg
				return nil, true, nil
			}
			if ae.Status == http.StatusRequestedRangeNotSatisfiable {
				return nil, true, nil
			}
		}
		return nil, false, err
	}

	switch {
	case o.Grep != "":
		q.Set("q", o.Grep)
		if o.IgnoreCase {
			q.Set("qi", "true")
		}
		resp, stop, err := get(q, "")
		if err != nil {
			return nil, err
		}
		if stop {
			return res, nil
		}
		lines := format.CleanLog(sanitize(resp.Body), false)
		res.setLines(lines)
		shown := lines
		if len(shown) > maxGrepMatches {
			shown = shown[:maxGrepMatches]
		}
		res.Header += fmt.Sprintf(" | matches %d of %d lines containing %q", len(shown), len(lines), o.Grep)
		if len(lines) == 0 {
			res.Text = "no matches"
			return res, nil
		}
		text, cut := capHeadLines(joinLines(shown), o.MaxChars)
		res.Text = text + "\n" + grepNoContext
		if cut || len(shown) < len(lines) || resp.Truncated {
			res.Text += "\n" + fmt.Sprintf("[truncated: %d of %d matching lines; narrow grep or pass step=]", len(shown), len(lines))
		}
		return res, nil

	case o.Offset != nil:
		off := *o.Offset
		// A bounded range: the chunk is read from its head, and next_offset
		// continues right after the shown part, so nothing is skipped.
		span := int64(2 * o.MaxChars)
		resp, stop, err := get(q, fmt.Sprintf("bytes=%d-%d", off, off+span-1))
		if err != nil {
			return nil, err
		}
		if stop {
			if res.NoLog == "" {
				res.Text = "no new output"
				res.NextOffset = off
			}
			return res, nil
		}
		body := resp.Body
		a, b, total := rangeOf(resp)
		more := resp.Truncated
		if resp.Status == http.StatusPartialContent {
			if total >= 0 {
				more = more || b+1 < total
			} else {
				more = more || int64(len(body)) >= span
			}
		} else {
			// The server ignored the range: the body is the whole log.
			total = int64(len(body))
			a = min(off, total)
			body = body[a:]
		}
		res.Total = total
		res.Header += fmt.Sprintf(" | from offset %d", off)

		var lines []format.LogLine
		var text string
		for {
			if more {
				// end the chunk on a complete line
				if i := bytes.LastIndexByte(body, '\n'); i >= 0 {
					body = body[:i+1]
				}
			}
			lines = format.CleanLog(sanitize(body), false)
			var cut bool
			text, cut = capHeadLines(joinLines(lines), o.MaxChars)
			if !cut {
				break
			}
			// the cleaned text does not fit: shrink the chunk, the rest is
			// read with the next call
			i := bytes.LastIndexByte(body[:len(body)/2], '\n')
			if i < 0 {
				break
			}
			body = body[:i+1]
			more = true
		}
		res.setLines(lines)
		if more {
			res.NextOffset = a + int64(len(body))
		} else {
			res.NextOffset = max(total, a+int64(len(body)))
		}
		if len(lines) == 0 {
			res.Text = "no new output"
			if more {
				res.Text = "(no printable output in this chunk)"
			}
			return res, nil
		}
		res.Text = text
		if more {
			res.Text += "\n" + fmt.Sprintf("[truncated: bytes %d-%d of %s; pass offset=next_offset to continue]", a, res.NextOffset, sizeOrUnknown(total))
		}
		return res, nil

	case o.First > 0:
		resp, stop, err := get(q, fmt.Sprintf("bytes=0-%d", 2*o.MaxChars-1))
		if err != nil {
			return nil, err
		}
		if stop {
			return res, nil
		}
		_, b, total := rangeOf(resp)
		raw := sanitize(resp.Body)
		if resp.Status == http.StatusPartialContent {
			if b+1 < total {
				if i := strings.LastIndexByte(raw, '\n'); i >= 0 {
					raw = raw[:i]
				}
			}
		} else {
			total = int64(len(resp.Body))
		}
		res.Total = total
		lines := format.CleanLog(raw, false)
		res.setLines(lines)
		kept := lines
		if len(kept) > o.First {
			kept = kept[:o.First]
		}
		res.Header += fmt.Sprintf(" | %s total | first %d lines", format.Size(total), len(kept))
		text, cut := capHeadLines(joinLines(kept), o.MaxChars)
		res.Text = text
		if cut || len(kept) < len(lines) || resp.Status == http.StatusPartialContent && int64(len(resp.Body)) < total {
			res.Text += "\n" + fmt.Sprintf("[truncated: first %d lines, %s of %s; pass first=N, grep=, or step=]", len(kept), format.Size(int64(len(text))), format.Size(total))
		}
		return res, nil
	}

	// default: tail
	resp, stop, err := get(q, fmt.Sprintf("bytes=-%d", 2*o.MaxChars))
	if err != nil {
		return nil, err
	}
	if stop {
		if res.NoLog == "" {
			res.Header += " | empty log"
		}
		return res, nil
	}
	var partial bool
	var total int64
	if resp.Status == http.StatusPartialContent {
		a, _, t := rangeOf(resp)
		partial, total = a > 0, t
		if t < 0 {
			total = int64(len(resp.Body))
		}
	} else {
		total = int64(len(resp.Body))
	}
	res.Total = total
	res.NextOffset = total
	lines := format.CleanLog(sanitize(resp.Body), partial)
	res.setLines(lines)
	kept := min(len(lines), o.Tail)
	res.Header += fmt.Sprintf(" | %s total | last %d lines", format.Size(total), kept)
	res.Text = tailText(lines, o.Tail, o.MaxChars, total, logTruncatedHint, partial)
	return res, nil
}

func (l *JobLog) setLines(lines []format.LogLine) {
	l.Numbered = lines
	l.Lines = make([]string, len(lines))
	for i, ln := range lines {
		l.Lines[i] = ln.Text
	}
}

// tailText keeps the last n lines, enforces maxChars from the end (marker
// first) and appends a `[truncated: ...]` trailer when the text does not cover
// the whole log.
func tailText(lines []format.LogLine, n, maxChars int, total int64, hint string, partial bool) string {
	kept := format.TailLines(lines, n)
	text := joinLines(kept)
	capped := false
	if utf8.RuneCountInString(text) > maxChars {
		text = format.CapTail(text, maxChars)
		capped = true
	}
	if partial || capped || len(kept) < len(lines) {
		shown := len(text)
		out := text + "\n" + fmt.Sprintf("[truncated: last %d lines, %s of %s; %s]", len(kept), format.Size(int64(shown)), sizeOrUnknown(total), hint)
		return out
	}
	return text
}

func sizeOrUnknown(total int64) string {
	if total < 0 {
		return "unknown size"
	}
	return format.Size(total)
}

func joinLines(lines []format.LogLine) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = l.Text
	}
	return strings.Join(parts, "\n")
}

// capHeadLines keeps the beginning of s within max runes, cut at a line
// boundary.
func capHeadLines(s string, max int) (string, bool) {
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	lines := strings.Split(s, "\n")
	used := 0
	var out []string
	for _, l := range lines {
		need := utf8.RuneCountInString(l) + 1
		if used+need > max {
			break
		}
		used += need
		out = append(out, l)
	}
	if len(out) == 0 {
		return string([]rune(lines[0])[:max]), true
	}
	return strings.Join(out, "\n"), true
}

func sanitize(b []byte) string { return strings.ToValidUTF8(string(b), "") }

// rangeOf parses Content-Range of a 206 answer; (0, 0, -1) when absent.
func rangeOf(resp *forgejo.Response) (a, b, total int64) {
	m := reContentRange.FindStringSubmatch(resp.Header.Get("Content-Range"))
	if m == nil {
		return 0, 0, -1
	}
	a, _ = strconv.ParseInt(m[1], 10, 64)
	b, _ = strconv.ParseInt(m[2], 10, 64)
	total = -1
	if m[3] != "*" {
		total, _ = strconv.ParseInt(m[3], 10, 64)
	}
	return a, b, total
}

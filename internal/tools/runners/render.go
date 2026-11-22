package runners

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
)

// levelWord is the scope word of a runner line: repo when repo_id != 0,
// owner when owner_id != 0, else global.
func levelWord(r apiRunner) string {
	switch {
	case r.RepoID != 0:
		return "repo"
	case r.OwnerID != 0:
		return "owner"
	}
	return "global"
}

// runnerLine renders `runner 7 ci-1 idle | repo | v6.3.1 | [docker] | eph`.
func runnerLine(r apiRunner) string {
	var b format.B
	b.Line(
		fmt.Sprintf("runner %d %s %s", r.ID, safeName(r.Name), r.Status),
		levelWord(r),
		versionWord(r.Version),
		format.Tags(r.Labels),
		ephWord(r.Ephemeral),
	)
	return strings.TrimRight(b.String(), "\n")
}

// safeName makes a free-form runner name safe for the one-line, " | "
// separated format: whitespace collapses and "|" becomes "/".
func safeName(n string) string {
	return strings.ReplaceAll(format.OneLine(n, 0), "|", "/")
}

func versionWord(v string) string {
	if v == "" {
		return ""
	}
	if v[0] >= '0' && v[0] <= '9' {
		return "v" + v
	}
	return v
}

func ephWord(e bool) string {
	if e {
		return "eph"
	}
	return ""
}

// runnerBlock renders the single-object view of `get`.
func runnerBlock(r apiRunner) string {
	var b format.B
	b.Raw(fmt.Sprintf("runner %d\n", r.ID))
	b.KV("name", safeName(r.Name))
	b.KV("status", r.Status)
	b.KV("level", levelWord(r))
	b.KV("uuid", r.UUID)
	b.KV("version", versionWord(r.Version))
	b.KV("labels", format.Tags(r.Labels))
	b.KV("ephemeral", r.Ephemeral)
	b.KV("description", r.Description)
	b.KV("repo_id", r.RepoID)
	b.KV("owner_id", r.OwnerID)
	return b.String()
}

// jobLine renders `job 5678 build waiting on:docker repo_id:12`.
func jobLine(j apiJob) string {
	parts := []string{fmt.Sprintf("job %d", j.ID)}
	if j.Name != "" {
		parts = append(parts, j.Name)
	}
	if j.Status != "" {
		parts = append(parts, j.Status)
	}
	if len(j.RunsOn) > 0 {
		parts = append(parts, "on:"+strings.Join(j.RunsOn, ","))
	}
	if j.RepoID != 0 {
		parts = append(parts, fmt.Sprintf("repo_id:%d", j.RepoID))
	}
	return strings.Join(parts, " ")
}

// queueSummary renders `waiting: 2, running: 1 | by runs-on: docker 2, ubuntu 1`.
func queueSummary(jobs []apiJob) string {
	waiting, running := 0, 0
	by := map[string]int{}
	for _, j := range jobs {
		switch j.Status {
		case "waiting":
			waiting++
		case "running":
			running++
		}
		key := strings.Join(j.RunsOn, ",")
		if key == "" {
			key = "(none)"
		}
		by[key]++
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if by[keys[i]] != by[keys[j]] {
			return by[keys[i]] > by[keys[j]]
		}
		return keys[i] < keys[j]
	})
	s := fmt.Sprintf("waiting: %d, running: %d", waiting, running)
	if len(keys) > 0 {
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%s %d", k, by[k])
		}
		s += " | by runs-on: " + strings.Join(parts, ", ")
	}
	return s
}

// jsonString marshals s without HTML escaping.
func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(buf.String(), "\n")
}

// renderRunnerFile renders the one-line `.runner` JSON the daemon reads. The
// key order is fixed. The `labels` key is omitted when labels is empty. The
// schema of the file is item 15.1/13 of the design (checked against the
// pinned forgejo-runner by the e2e test).
func renderRunnerFile(id int64, uuid, name, token, address string, labels []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `{"id":%d,"uuid":%s,"name":%s,"token":%s,"address":%s`,
		id, jsonString(uuid), jsonString(name), jsonString(token), jsonString(address))
	if len(labels) > 0 {
		parts := make([]string, len(labels))
		for i, l := range labels {
			parts[i] = jsonString(l)
		}
		sb.WriteString(`,"labels":[` + strings.Join(parts, ",") + `]`)
	}
	sb.WriteString("}")
	return sb.String()
}

// RenderRunnerFile is the exported entry of renderRunnerFile for the e2e test
// (test/integration), so the text the tool prints is exactly what the test
// writes and starts a runner with.
func RenderRunnerFile(id int64, uuid, name, token, address string, labels []string) string {
	return renderRunnerFile(id, uuid, name, token, address, labels)
}

// ExtractRunnerFile returns the JSON of the `.runner:` line of a
// `runner register` result.
func ExtractRunnerFile(text string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, ".runner: "); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

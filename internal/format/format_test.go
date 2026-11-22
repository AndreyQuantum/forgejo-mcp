package format

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite golden files")

// golden compares got with testdata/<name>.golden (local helper: this package
// imports nothing internal).
func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != got {
		t.Errorf("golden %s mismatch\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func TestBuilder(t *testing.T) {
	var b B
	b.KV("state", "open")
	b.KV("empty", "")
	b.KV("zero", 0)
	b.KV("flag", false)
	b.KV("n", 3)
	b.KV("body", "line1\nline2\n")
	b.Line("a", "", "b")
	b.Line("", "")
	b.Raw("raw\n")
	want := "state: open\nn: 3\nbody: line1\n  line2\na | b\nraw\n"
	if got := b.String(); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestListHeader(t *testing.T) {
	cases := []struct {
		kind string
		p    Page
		want string
	}{
		{"prs", Page{Page: 1, Limit: 20, Total: 134, Shown: 20, More: true}, "# prs 20/134 page 1/7 next: page=2"},
		{"prs", Page{Page: 7, Limit: 20, Total: 134, Shown: 14}, "# prs 14/134 page 7/7"},
		{"prs", Page{Page: 1, Limit: 20, Total: 0}, "# prs 0/0"},
		{"events", Page{Page: 2, Limit: 20, Total: -1, Shown: 20, More: true}, "# events 20 page 2 next: page=3"},
		{"events", Page{Page: 1, Limit: 20, Total: -1, Shown: 3}, "# events 3 page 1"},
	}
	for _, c := range cases {
		if got := ListHeader(c.kind, c.p); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestTruncAndOneLine(t *testing.T) {
	if got := Trunc("abc", 5, "x"); got != "abc" {
		t.Errorf("no cut: %q", got)
	}
	got := Trunc("héllo wörld", 4, "pass full=true")
	if got != "héll\n[truncated: 4 of 11 chars; pass full=true]" {
		t.Errorf("got %q", got)
	}
	if !utf8.ValidString(Trunc("日本語日本語", 2, "h")) {
		t.Error("invalid utf8")
	}
	if got := OneLine("a \n\t b   c", 100); got != "a b c" {
		t.Errorf("got %q", got)
	}
	if got := OneLine("abcdefghij", 5); got != "abcd…" {
		t.Errorf("got %q", got)
	}
	if got := OneLine("日本語日本語", 3); got != "日本…" {
		t.Errorf("got %q", got)
	}
}

func TestSmallHelpers(t *testing.T) {
	if SHA("0123456789", 8) != "01234567" || SHA("abc", 8) != "abc" {
		t.Error("SHA")
	}
	if Tags(nil) != "" || Tags([]string{"a", "b"}) != "[a,b]" {
		t.Error("Tags")
	}
	if Logins(nil) != "" || Logins([]string{"a", "b"}) != "@a,@b" {
		t.Error("Logins")
	}
	for in, want := range map[int64]string{0: "0B", 999: "999B", 1200: "1.2KB", 17200: "17.2KB", 1200000: "1.2MB"} {
		if got := Size(in); got != want {
			t.Errorf("Size(%d)=%s want %s", in, got, want)
		}
	}
}

func TestTimeHelpers(t *testing.T) {
	if got := T("2026-11-22T10:15:42+03:00"); got != "2026-11-22 07:15" {
		t.Errorf("T: %q", got)
	}
	if T("") != "" || T("garbage") != "garbage" {
		t.Error("T edge")
	}
	for d, want := range map[time.Duration]string{
		0: "0s", 42 * time.Second: "42s", 63 * time.Second: "1m03s",
		2*time.Hour + 5*time.Minute + 9*time.Second: "2h05m", 1500 * time.Millisecond: "2s",
	} {
		if got := Dur(d); got != want {
			t.Errorf("Dur(%v)=%s want %s", d, got, want)
		}
	}
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		30 * time.Second: "30s", 5 * time.Minute: "5m", 3 * time.Hour: "3h",
		72 * time.Hour: "3d", 120 * 24 * time.Hour: "4mo", 800 * 24 * time.Hour: "2y",
	} {
		if got := Age(now.Add(-d), now); got != want {
			t.Errorf("Age(%v)=%s want %s", d, got, want)
		}
	}
}

func TestCapHead(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("line number ")
		sb.WriteString(strings.Repeat("x", 20))
		sb.WriteByte('\n')
	}
	sb.WriteString("[truncated: 10 of 20 files shown; raise max_chars]\nnext_offset: 1234\n")
	got := CapHead(sb.String(), 1000)
	if utf8.RuneCountInString(got) > 1000 {
		t.Errorf("too long: %d", utf8.RuneCountInString(got))
	}
	if !strings.HasSuffix(got, "[truncated: 10 of 20 files shown; raise max_chars]\nnext_offset: 1234") {
		t.Errorf("trailers lost: %q", got[len(got)-120:])
	}
	if !strings.Contains(got, "[truncated: output cut at 1000 chars") {
		t.Errorf("marker missing")
	}
	if !strings.HasPrefix(got, "line number x") {
		t.Error("head not kept")
	}
	if CapHead("short", 100) != "short" {
		t.Error("short text changed")
	}
}

func TestCapTail(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 100; i++ {
		sb.WriteString("row ")
		sb.WriteString(strings.Repeat("y", 20))
		sb.WriteByte('\n')
	}
	sb.WriteString("THE END")
	got := CapTail(sb.String(), 500)
	if utf8.RuneCountInString(got) > 500 {
		t.Errorf("too long")
	}
	if !strings.HasPrefix(got, "[truncated: first ") || !strings.HasSuffix(got, "THE END") {
		t.Errorf("got %q", got)
	}
	long := CapTail(strings.Repeat("z", 1000)+"END", 200)
	if !strings.HasSuffix(long, "END") || utf8.RuneCountInString(long) > 200 {
		t.Errorf("single long line: %d", utf8.RuneCountInString(long))
	}
}

func TestCleanLog(t *testing.T) {
	raw := "partial tail of a line\n" +
		"2026-11-22T10:00:00.123Z \x1b[31mred text\x1b[0m\n" +
		"2026-11-22T10:00:01Z progress 10%\rprogress 50%\rprogress 100%\n" +
		"2026-11-22T10:00:02Z ::group::Run tests\n" +
		"2026-11-22T10:00:03Z \n" +
		"2026-11-22T10:00:04Z same\n" +
		"2026-11-22T10:00:05Z same\n" +
		"2026-11-22T10:00:06Z same\n" +
		"2026-11-22T10:00:07Z ::endgroup::\n" +
		"windows line\r\n" +
		"last"
	got := CleanLog(raw, true)
	var texts []string
	for _, l := range got {
		texts = append(texts, l.Text)
	}
	want := []string{"red text", "progress 100%", "> Run tests", "same (x3)", "windows line", "last"}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Errorf("got %q want %q", texts, want)
	}
	if got[0].N != 2 || got[3].N != 6 || got[5].N != 11 {
		t.Errorf("line numbers: %+v", got)
	}
	all := CleanLog("a\nb", false)
	if len(all) != 2 || all[0].N != 1 {
		t.Errorf("partialFirst=false: %+v", all)
	}
	if len(CleanLog("", false)) != 0 {
		t.Error("empty input")
	}
	tail := TailLines(got, 2)
	if len(tail) != 2 || tail[1].Text != "last" || len(TailLines(got, 0)) != len(got) {
		t.Errorf("tail: %+v", tail)
	}
}

func TestErrorMatches(t *testing.T) {
	lines := CleanLog("ok line\nFAIL TestX (0.01s)\n    x_test.go:44: expected 200, got 500\nexit status 1\nError: Process completed with exit code 1.\npanic: boom\n", false)
	m := ErrorMatches(lines, 10)
	if len(m) != 2 || m[0].Text != "FAIL TestX (0.01s)" || m[1].Text != "panic: boom" || m[0].N != 2 {
		t.Errorf("got %+v", m)
	}
	if got := ErrorMatches(lines, 1); len(got) != 1 {
		t.Errorf("max not honoured: %+v", got)
	}
}

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func compact(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStripNoise(t *testing.T) {
	in := decode(t, `{
		"id": 7, "name": "x", "zero": 0, "no": false, "nil": null, "empty": "", "list": [], "obj": {},
		"url": "https://h/api/1", "avatar_url": "a", "issue_url": "b", "html_url": "https://h/o/r/1",
		"target_url": "t", "clone_url": "c", "browser_download_url": "d",
		"permissions": {"admin": false, "push": false},
		"user": {"id": 1, "login": "alice", "avatar_url": "x"},
		"merged_by": {"login": "bob"},
		"repository": {"full_name": "o/r", "id": 3},
		"head": {"repo": {"full_name": "o/f", "id": 4}, "user": {"id": 9}},
		"event_payload": "{}",
		"labels": [{"name": "bug", "url": "u"}]
	}`)
	out, total := StripNoise(in)
	if total != -1 {
		t.Errorf("total %d", total)
	}
	want := `{"head":{"repo":"o/f","user":{"id":9}},"html_url":"https://h/o/r/1","id":7,` +
		`"labels":[{"name":"bug"}],"merged_by":"bob","name":"x","no":false,"repository":"o/r",` +
		`"target_url":"t","user":"alice","zero":0,"browser_download_url":"d","clone_url":"c"}`
	got := decode(t, compact(t, out))
	if compact(t, got) != compact(t, decode(t, want)) {
		t.Errorf("got  %s\nwant %s", compact(t, got), compact(t, decode(t, want)))
	}
	keep, _ := StripNoise(decode(t, `{"permissions":{"admin":true,"push":false}}`))
	if compact(t, keep) != `{"permissions":{"admin":true,"push":false}}` {
		t.Errorf("permissions: %s", compact(t, keep))
	}
}

func TestStripNoiseWrappers(t *testing.T) {
	out, total := StripNoise(decode(t, `{"workflow_runs":[{"id":1,"url":"u"},{"id":2}],"total_count":134}`))
	if total != 134 || compact(t, out) != `[{"id":1},{"id":2}]` {
		t.Errorf("runs: %d %s", total, compact(t, out))
	}
	out, total = StripNoise(decode(t, `{"ok":true,"data":[{"login":"a","id":1}]}`))
	if total != -1 || compact(t, out) != `[{"id":1,"login":"a"}]` {
		t.Errorf("ok/data: %d %s", total, compact(t, out))
	}
	out, total = StripNoise(decode(t, `{"total_count":3,"a":1}`))
	if total != -1 || compact(t, out) != `{"a":1,"total_count":3}` {
		t.Errorf("non wrapper: %d %s", total, compact(t, out))
	}
}

func TestPick(t *testing.T) {
	obj := decode(t, `{"number":42,"title":"Fix","user":{"login":"alice","id":1},
		"labels":[{"name":"bug","id":1},{"name":"ui","id":2}],"assignees":[{"login":"a"},{"login":"b"}]}`)
	cases := []struct {
		paths []string
		want  string
	}{
		{
			[]string{"number", "title", "user.login:by", "labels[].name:labels"},
			`{"number":42,"title":"Fix","by":"alice","labels":["bug","ui"]}`,
		},
		{[]string{"assignees[1].login", "labels[].name"}, `{"assignees[1].login":"b","labels[].name":["bug","ui"]}`},
		{[]string{"user.*"}, `{"user.id":1,"user.login":"alice"}`},
		{[]string{"missing", "user.nope", "labels[5].name", "number"}, `{"number":42}`},
		{[]string{"number,title"}, `{"number":42,"title":"Fix"}`},
		{[]string{"title:t", "number:t"}, `{"t":42}`},
	}
	for _, c := range cases {
		got, err := Pick(obj, c.paths)
		if err != nil || got != c.want {
			t.Errorf("%v: got %q err %v want %q", c.paths, got, err, c.want)
		}
	}
	arr := decode(t, `[{"id":1,"a":"x"},{"id":2},{"a":"z"}]`)
	got, err := Pick(arr, []string{"id", "a"})
	if err != nil || got != "{\"id\":1,\"a\":\"x\"}\n{\"id\":2}\n{\"a\":\"z\"}" {
		t.Errorf("array: %q %v", got, err)
	}
	html, _ := Pick(map[string]any{"t": "a<b&c"}, []string{"t"})
	if html != `{"t":"a<b&c"}` {
		t.Errorf("html escaped: %s", html)
	}
}

func TestPickErrors(t *testing.T) {
	cases := map[string]string{
		"a..b":     "pick: empty segment at 3",
		"a[":       "pick: missing ']' at 2",
		"a[x]":     "pick: bad index at 3",
		"a[-1]":    "pick: bad index at 3",
		".a":       "pick: empty segment at 1",
		"a.":       "pick: empty segment at 3",
		"a:":       "pick: empty alias at 2",
		"*.a":      "pick: '*' must be the last segment at 1",
		"us*r":     "pick: '*' must be a whole segment at 1",
		"a]":       "pick: unexpected \"]\" at 2",
		"user.*:u": "pick: alias not allowed with '*' at 7",
	}
	for in, want := range cases {
		_, err := Pick(map[string]any{}, []string{in})
		if err == nil || err.Error() != want {
			t.Errorf("%q: got %v want %q", in, err, want)
		}
	}
	if _, err := Pick("scalar", []string{"a"}); err == nil {
		t.Error("scalar root must fail")
	}
}

const sampleDiff = `diff --git a/src/a.go b/src/a.go
index 1111111..2222222 100644
--- a/src/a.go
+++ b/src/a.go
@@ -1,3 +1,4 @@
 package a
-var x = 1
+var x = 2
+var y = 3
\ No newline at end of file
diff --git a/old name.txt b/new name.txt
similarity index 90%
rename from old name.txt
rename to new name.txt
index 3333333..4444444 100644
--- a/old name.txt
+++ b/new name.txt
@@ -1 +1 @@
-hello
+hello world
diff --git a/logo.png b/logo.png
index 5555555..6666666 100644
Binary files a/logo.png and b/logo.png differ
diff --git a/gone.md b/gone.md
deleted file mode 100644
index 7777777..0000000
--- a/gone.md
+++ /dev/null
@@ -1,2 +0,0 @@
---- dashes
-b
diff --git a/docs/new.md b/docs/new.md
new file mode 100644
index 0000000..8888888
--- /dev/null
+++ b/docs/new.md
@@ -0,0 +1 @@
+fresh
`

func TestSplitDiff(t *testing.T) {
	files := SplitDiff(sampleDiff)
	if len(files) != 5 {
		t.Fatalf("got %d files", len(files))
	}
	type exp struct {
		path, old, status string
		bin               bool
		a, r              int
	}
	want := []exp{
		{"src/a.go", "", "M", false, 2, 1},
		{"new name.txt", "old name.txt", "R", false, 1, 1},
		{"logo.png", "", "M", true, 0, 0},
		{"gone.md", "", "D", false, 0, 2},
		{"docs/new.md", "", "A", false, 1, 0},
	}
	for i, w := range want {
		f := files[i]
		if f.Path != w.path || f.OldPath != w.old || f.Status != w.status || f.Binary != w.bin || f.Added != w.a || f.Removed != w.r {
			t.Errorf("file %d: %+v want %+v", i, f, w)
		}
	}
	if !strings.HasPrefix(files[0].Text, "diff --git a/src/a.go") || !strings.Contains(files[0].Text, "No newline") {
		t.Errorf("raw text: %q", files[0].Text)
	}
	if got := strings.Join([]string{files[0].Text, files[1].Text, files[2].Text, files[3].Text, files[4].Text}, ""); got != sampleDiff {
		t.Error("sections do not reassemble to the input")
	}
	if len(SplitDiff("")) != 0 || len(SplitDiff("no diff here\n")) != 0 {
		t.Error("empty/garbage input")
	}
}

func TestSplitDiffQuotedAndCRLF(t *testing.T) {
	d := "diff --git \"a/f\\303\\251.go\" \"b/f\\303\\251.go\"\r\n--- \"a/f\\303\\251.go\"\r\n+++ \"b/f\\303\\251.go\"\r\n@@ -1 +1 @@\r\n-a\r\n+b\r\n"
	files := SplitDiff(d)
	if len(files) != 1 || files[0].Path != "fé.go" || files[0].Added != 1 || files[0].Removed != 1 {
		t.Errorf("%+v", files)
	}
	out := RenderDiff(files, 1000)
	if strings.Contains(out, "\r") {
		t.Errorf("CR left: %q", out)
	}
}

func TestFilterDiff(t *testing.T) {
	files := SplitDiff(sampleDiff)
	names := func(fs []FileDiff) string {
		var n []string
		for _, f := range fs {
			n = append(n, f.Path)
		}
		return strings.Join(n, ",")
	}
	cases := []struct {
		globs []string
		want  string
	}{
		{nil, "src/a.go,new name.txt,logo.png,gone.md,docs/new.md"},
		{[]string{"*.md"}, "gone.md,docs/new.md"},
		{[]string{"src/*.go"}, "src/a.go"},
		{[]string{"a.go"}, "src/a.go"},
		{[]string{"old name.txt"}, "new name.txt"},
		{[]string{"docs/"}, "docs/new.md"},
		{[]string{"[bad"}, ""},
		{[]string{"logo.png", "gone.md"}, "logo.png,gone.md"},
	}
	for _, c := range cases {
		if got := names(FilterDiff(files, c.globs)); got != c.want {
			t.Errorf("%v: got %q want %q", c.globs, got, c.want)
		}
	}
}

func TestRenderDiffGolden(t *testing.T) {
	files := SplitDiff(sampleDiff)
	golden(t, "diff_all", RenderDiff(files, 14000))
	golden(t, "diff_binary_only", RenderDiff(FilterDiff(files, []string{"*.png"}), 14000))
}

func TestRenderDiffCaps(t *testing.T) {
	var big strings.Builder
	big.WriteString("diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -1,300 +1,300 @@\n")
	for i := 0; i < 300; i++ {
		big.WriteString("+line of added text number " + strings.Repeat("z", 10) + "\n")
	}
	var many strings.Builder
	for i := 0; i < 20; i++ {
		name := string(rune('a'+i)) + ".go"
		many.WriteString("diff --git a/" + name + " b/" + name + "\n--- a/" + name + "\n+++ b/" + name + "\n@@ -1 +1 @@\n-" + strings.Repeat("o", 80) + "\n+" + strings.Repeat("n", 80) + "\n")
	}

	// A lone big file is cut with the per-file marker.
	out := RenderDiff(SplitDiff(big.String()), 2000)
	if !strings.Contains(out, "[truncated: file shown ") || !strings.Contains(out, "and a larger max_chars]") {
		t.Errorf("per-file marker missing: %s", out)
	}
	if utf8.RuneCountInString(out) > 2000 {
		t.Errorf("lone file over budget: %d", utf8.RuneCountInString(out))
	}
	if !strings.HasPrefix(out, "=== M big.txt (+300 -0) ===\n@@ -1,300") {
		t.Errorf("header: %q", out[:60])
	}

	// Many files: stop at the budget, trailer outside it, names capped at 15.
	files := SplitDiff(many.String())
	out = RenderDiff(files, 1050)
	body, trailer, _ := strings.Cut(out, "\n[truncated: ")
	if utf8.RuneCountInString(body) > 1050 {
		t.Errorf("body over budget: %d", utf8.RuneCountInString(body))
	}
	if !strings.HasPrefix(trailer, "5 of 20 files shown, ") || !strings.HasSuffix(trailer, "pass paths=[...] or raise max_chars]") {
		t.Errorf("trailer: %q", trailer)
	}
	if !strings.Contains(trailer, "omitted: f.go, g.go") || strings.Contains(trailer, "more") || strings.Count(trailer, ".go") != 15 {
		t.Errorf("names: %q", trailer)
	}
	// More than 15 omitted names are summarised.
	out = RenderDiff(files, 700)
	if !strings.Contains(out, ", +") || !strings.Contains(out, " more; pass paths") {
		t.Errorf("name cap: %q", out)
	}
	// A rename is shown old->new; an empty list renders nothing.
	if RenderDiff(nil, 100) != "" {
		t.Error("empty")
	}
	r := RenderDiff(SplitDiff(sampleDiff)[1:2], 5000)
	if !strings.HasPrefix(r, "=== R old name.txt->new name.txt (+1 -1) ===") {
		t.Errorf("rename header: %q", r)
	}
}

func TestGoldenRenders(t *testing.T) {
	// A list and single object built only from the helpers, as the area
	// renderers do, to pin the shared line conventions.
	var b B
	b.Raw(ListHeader("prs", Page{Page: 1, Limit: 2, Total: 5, Shown: 2, More: true}) + "\n")
	b.Line("#42 open", OneLine("Fix   the\nlogin", 120), "by alice", Tags([]string{"bug", "ui"}), Logins([]string{"bob", "eve"}), "upd "+T("2026-11-22T10:15:00Z"))
	b.Line("#41 merged", "Docs", "by bob", Tags(nil), Logins(nil), "upd "+T("2026-11-21T08:00:00Z"))
	b.KV("body", Trunc("0123456789\nabcdef", 12, "pass body_max=0"))
	golden(t, "render_list", b.String())

	log := CleanLog("2026-11-22T10:00:00Z ::group::Run tests\n2026-11-22T10:00:01Z FAIL TestSessionRefresh (0.02s)\n2026-11-22T10:00:02Z     session_test.go:44: expected 200, got 500\n2026-11-22T10:00:03Z exit status 1\n", false)
	var lb B
	for _, l := range ErrorMatches(log, 12) {
		lb.Line(strconv.Itoa(l.N) + ": " + l.Text)
	}
	for _, l := range TailLines(log, 3) {
		lb.Raw(l.Text + "\n")
	}
	golden(t, "render_log", lb.String())
}

func TestUnwrapThenPickKeepsNestedPaths(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(`{"total_count":1,"issues":[{"number":3,"title":"t","user":{"login":"alice","id":4},"labels":[{"name":"bug"}]}]}`), &v); err != nil {
		t.Fatal(err)
	}
	un, total := Unwrap(v)
	if total != 1 {
		t.Fatalf("total = %d", total)
	}
	got, err := Pick(un, []string{"number", "user.login:by", "labels[].name:labels"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"by":"alice"`, `"number":3`, `bug`} {
		if !strings.Contains(got, want) {
			t.Errorf("pick output %q lacks %s", got, want)
		}
	}
	// documents why the order matters: stripped input loses the path
	stripped, _ := StripNoise(v)
	lost, _ := Pick(stripped, []string{"user.login:by"})
	if strings.Contains(lost, "alice") {
		t.Errorf("StripNoise then Pick unexpectedly kept by: %s", lost)
	}
}

// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for the tools of the checks: with
// GO_AUDIT_HELPER set, it does what helper says instead of running tests.
func TestMain(m *testing.M) {
	if os.Getenv("GO_AUDIT_HELPER") != "" {
		os.Exit(helper(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// helper does what its arguments say:
//
//	sleep SECONDS EVENTS NAME  sleeps, writing "NAME start|end NANOSECONDS"
//	                           lines to the file EVENTS around the sleep
//	print TEXT                 prints TEXT
//	fail CODE TEXT             prints TEXT to stderr and exits with CODE
func helper(args []string) int {
	switch {
	case len(args) == 4 && args[0] == "sleep":
		secs, _ := strconv.ParseFloat(args[1], 64)
		event := func(what string) {
			f, err := os.OpenFile(args[2], os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
			if err != nil {
				panic(err)
			}
			_, err = fmt.Fprintf(f, "%s %s %d\n", args[3], what, time.Now().UnixNano())
			if err := errors.Join(err, f.Close()); err != nil {
				panic(err)
			}
		}
		event("start")
		time.Sleep(time.Duration(secs * float64(time.Second)))
		event("end")
		return 0
	case len(args) == 2 && args[0] == "print":
		fmt.Println(args[1])
		return 0
	case len(args) == 3 && args[0] == "fail":
		fmt.Fprintln(os.Stderr, args[2])
		code, _ := strconv.Atoi(args[1])
		return code
	}
	fmt.Fprintf(os.Stderr, "helper: bad arguments %q\n", args)
	return 99
}

// helperSetup makes a module to audit and a store whose config has the
// checks, each {name, format, helper arguments…}, in the order given. A
// check named off-… is off; one of the format "missing" runs a program that
// is not there. It returns the store.
func helperSetup(t *testing.T, checks ...[]string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("checks:\n")
	for _, c := range checks {
		name, format, cmd := c[0], c[1], []string{strconv.Quote(exe)}
		if format == "missing" {
			format, cmd = "diff", []string{"no-such-tool"}
		}
		for _, arg := range c[2:] {
			cmd = append(cmd, strconv.Quote(arg))
		}
		fmt.Fprintf(&b, "  - name: %s\n    cmd: [%s]\n    format: %s\n",
			name, strings.Join(cmd, ", "), format)
		if strings.HasPrefix(c[0], "off-") {
			b.WriteString("    off: true\n")
		}
	}
	store := t.TempDir()
	writeFile(t, filepath.Join(store, configName), b.String())
	t.Setenv("GO_STYLE_HOME", store)
	t.Setenv("GO_AUDIT_HELPER", "1")
	t.Chdir(testModule(t, map[string]string{"a.go": "package m\n"}))
	return store
}

// auditOut runs go-audit and returns what it printed.
func auditOut(t *testing.T, ctx context.Context, c int, only string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	saved := stdout
	stdout = &out
	defer func() { stdout = saved }()
	err := runAudit(ctx, c, only, "", args)
	return out.String(), err
}

func TestAuditStatuses(t *testing.T) {
	store := helperSetup(t,
		[]string{"fine", "diff", "print", ""},
		[]string{"changed", "diff", "print", "something changed"},
		[]string{"broken", "diff", "fail", "3", "it broke"},
		[]string{"absent", "missing"},
		[]string{"off-one", "diff", "print", "should not run"},
	)
	out, err := auditOut(t, t.Context(), 1, "")
	if code, ok := errors.AsType[exitStatus](err); !ok || code != exitFailure {
		t.Errorf("an audit with a failed check: err = %v, want exit status %d", err, exitFailure)
	}
	lines := strings.Split(out, "\n")
	for i, want := range []string{
		"  fine    ok          0",
		"  changed found       1",
		"  broken  error       -",
		"  absent  skipped     -      -  no-such-tool is not in PATH",
	} {
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], want) {
			t.Errorf("line %d of the summary is not %q:\n%s", i+1, want, out)
		}
	}
	if !strings.Contains(out, "  broken  error       -") || !strings.Contains(out, "it broke") {
		t.Errorf("the failed check shows no reason:\n%s", out)
	}
	if strings.Contains(out, "off-one") {
		t.Errorf("a check that is off ran:\n%s", out)
	}

	reports, err := loadReports(store)
	if err != nil || len(reports) != 1 {
		t.Fatalf("store holds %d reports, %v; want 1", len(reports), err)
	}
	r := reports[0]
	if r.err != nil || r.Kind != "audit" || r.Module != "example.com/m" || r.Label != "./..." ||
		r.Total != 1 || r.Summary != "changed 1 · broken error · 1 skipped" {
		t.Errorf("report = %+v (err %v)", r, r.err)
	}
	md, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# go-audit: example.com/m",
		"| broken | error | – | ", "| it broke |",
		"| absent | skipped | – | – | no-such-tool is not in PATH |",
		"## changed: 1 finding\n\n- something changed\n",
	} {
		if !strings.Contains(string(md), want) {
			t.Errorf("report:\n%s\nwant it to contain %q", md, want)
		}
	}

	// The output of the checks that ran is in the logs; the one that did not
	// run has none.
	logs := strings.TrimSuffix(strings.Replace(r.path, "reports", "runs", 1), ".md")
	for name, want := range map[string]string{"changed.out": "something changed", "broken.log": "it broke"} {
		if got, _ := os.ReadFile(filepath.Join(logs, name)); !strings.Contains(string(got), want) {
			t.Errorf("logs/%s = %q, want %q", name, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(logs, "absent.out")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a skipped check has output in the logs: %v", err)
	}
}

func TestAuditExitCodes(t *testing.T) {
	helperSetup(t,
		[]string{"fine", "diff", "print", ""},
		[]string{"changed", "diff", "print", "x"},
		[]string{"off-on-demand", "diff", "print", "ran"},
	)
	if _, err := auditOut(t, t.Context(), 1, "fine"); err != nil {
		t.Errorf("nothing found: err = %v, want none", err)
	}
	if _, err := auditOut(t, t.Context(), 1, ""); !errors.Is(err, exitStatus(1)) {
		t.Errorf("findings: err = %v, want exit status 1", err)
	}
	// -only runs a check that is off.
	out, err := auditOut(t, t.Context(), 1, "off-on-demand")
	if !errors.Is(err, exitStatus(1)) || !strings.Contains(out, "off-on-demand found") {
		t.Errorf("-only of a check that is off: err %v, printed:\n%s", err, out)
	}
	// Two runs in one second have reports of their own.
	reports, _ := loadReports(os.Getenv("GO_STYLE_HOME"))
	if len(reports) != 3 {
		t.Errorf("three runs left %d reports", len(reports))
	}
}

func TestAuditUsage(t *testing.T) {
	helperSetup(t, []string{"fine", "diff", "print", ""})
	for _, tt := range []struct {
		c    int
		only string
		args []string
		want string
	}{
		{0, "", nil, "-c is how many checks run at a time"},
		{1, "nope", nil, "-only: no check nope"},
		{1, "", []string{"../.."}, "is outside the module"},
	} {
		_, err := auditOut(t, t.Context(), tt.c, tt.only, tt.args...)
		if _, ok := errors.AsType[usageError](err); !ok || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("-c %d -only %q %q: err = %v, want a usage error with %q", tt.c, tt.only, tt.args, err, tt.want)
		}
	}
	t.Chdir(t.TempDir())
	if _, err := auditOut(t, t.Context(), 1, ""); err == nil || !strings.Contains(err.Error(), "not inside a Go module") {
		t.Errorf("outside a module: err = %v", err)
	}
}

// events reads the lines the sleeping helpers wrote: name → start and end.
func events(t *testing.T, path string) map[string][2]int64 {
	t.Helper()
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	ev := map[string][2]int64{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		var name, what string
		var at int64
		if _, err := fmt.Sscan(s.Text(), &name, &what, &at); err != nil {
			t.Fatalf("event %q: %v", s.Text(), err)
		}
		e := ev[name]
		if what == "start" {
			e[0] = at
		} else {
			e[1] = at
		}
		ev[name] = e
	}
	return ev
}

// busy returns how long at least one of the helpers slept, and the sum of
// their sleeps: the first is less than the second when they overlapped. The
// times are those of the helpers, so the start of the processes, slow under
// the race detector, does not count.
func busy(ev map[string][2]int64) (union, sum time.Duration) {
	spans := slices.SortedFunc(maps.Values(ev), func(a, b [2]int64) int {
		return cmp.Compare(a[0], b[0])
	})
	var end int64
	for _, s := range spans {
		sum += time.Duration(s[1] - s[0])
		start := max(s[0], end)
		if s[1] > start {
			union += time.Duration(s[1] - start)
		}
		end = max(end, s[1])
	}
	return union, sum
}

// mostAtOnce returns how many of the helpers slept at the same time at most.
func mostAtOnce(ev map[string][2]int64) int {
	most := 0
	for _, a := range ev {
		n := 0
		for _, b := range ev {
			if b[0] <= a[0] && a[0] < b[1] {
				n++
			}
		}
		most = max(most, n)
	}
	return most
}

func TestAuditConcurrency(t *testing.T) {
	evFile := filepath.Join(t.TempDir(), "events")
	// Sleeps of different lengths, so that the checks finish out of order.
	secs := map[string]string{"a": "0.4", "b": "0.1", "c": "0.3", "d": "0.1"}
	var checks [][]string
	for _, name := range slices.Sorted(maps.Keys(secs)) {
		checks = append(checks, []string{name, "diff", "sleep", secs[name], evFile, name})
	}
	helperSetup(t, checks...)

	for _, c := range []int{1, 2} {
		if err := os.Remove(evFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		out, err := auditOut(t, t.Context(), c, "")
		if err != nil {
			t.Fatalf("-c %d: %v\n%s", c, err, out)
		}
		// The lines are in the order of the config.
		var names []string
		for _, line := range strings.Split(out, "\n")[1:5] {
			names = append(names, strings.Fields(line)[0])
		}
		if !slices.Equal(names, []string{"a", "b", "c", "d"}) {
			t.Errorf("-c %d: lines in the order %v:\n%s", c, names, out)
		}
		ev := events(t, evFile)
		if got := mostAtOnce(ev); got != c {
			t.Errorf("-c %d: %d checks ran at once", c, got)
		}
		if union, sum := busy(ev); c > 1 && union >= sum {
			t.Errorf("-c %d: the checks took %v, no less than the %v of their sleeps one after another",
				c, union, sum)
		}
		if !strings.Contains(out, fmt.Sprintf("with -c %d", c)) {
			t.Errorf("-c %d: no wall time in\n%s", c, out)
		}
	}
}

func TestAuditInterrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no interrupt to send on Windows; the checks are killed")
	}
	evFile := filepath.Join(t.TempDir(), "events")
	store := helperSetup(t,
		[]string{"long1", "diff", "sleep", "30", evFile, "long1"},
		[]string{"long2", "diff", "sleep", "30", evFile, "long2"},
		[]string{"later", "diff", "sleep", "30", evFile, "later"},
	)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		// Interrupt once both running checks have started.
		for {
			data, _ := os.ReadFile(evFile)
			if bytes.Count(data, []byte(" start ")) >= 2 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	out, err := auditOut(t, ctx, 2, "")
	if took := time.Since(start); took > waitDelay {
		t.Errorf("an interrupted audit took %v to stop", took)
	}
	if !errors.Is(err, exitStatus(exitFailure)) || !strings.Contains(out, statusInterrupted) {
		t.Errorf("interrupted: err %v, printed:\n%s", err, out)
	}
	ev := events(t, evFile)
	if _, ok := ev["later"]; ok {
		t.Error("a check started after the interrupt")
	}
	for _, name := range []string{"long1", "long2"} {
		if ev[name][1] != 0 {
			t.Errorf("check %s slept to its end despite the interrupt", name)
		}
	}
	if reports, _ := loadReports(store); len(reports) != 0 {
		t.Errorf("an interrupted audit left %d reports", len(reports))
	}
}

func TestSelectChecks(t *testing.T) {
	cfg := &config{file: "mine.yml", Checks: []*check{
		{Name: "vet"}, {Name: "fix"}, {Name: "shadow", Off: true}, {Name: "gofmt"},
	}}
	tests := []struct {
		only, skip string
		want       []string
		err        string
	}{
		{"", "", []string{"vet", "fix", "gofmt"}, ""},
		{"gofmt,vet", "", []string{"vet", "gofmt"}, ""},
		{"shadow", "", []string{"shadow"}, ""},
		{"", "fix, vet", []string{"gofmt"}, ""},
		{"vet,fix", "fix", []string{"vet"}, ""},
		{"", "vet,fix,gofmt", nil, "no check is left to run"},
		{"lint", "", nil, "-only: no check lint in the mine.yml config (checks: vet, fix, shadow, gofmt)"},
		{"", "lint", nil, "-skip: no check lint"},
	}
	for _, tt := range tests {
		checks, err := selectChecks(cfg, tt.only, tt.skip)
		var names []string
		for _, ch := range checks {
			names = append(names, ch.Name)
		}
		if tt.err != "" {
			if _, ok := errors.AsType[usageError](err); !ok || !strings.HasPrefix(err.Error(), tt.err) {
				t.Errorf("-only %q -skip %q: err = %v, want a usage error %q", tt.only, tt.skip, err, tt.err)
			}
			continue
		}
		if err != nil || !slices.Equal(names, tt.want) {
			t.Errorf("-only %q -skip %q = %v, %v; want %v", tt.only, tt.skip, names, err, tt.want)
		}
	}
}

func TestRebase(t *testing.T) {
	root := filepath.FromSlash("/src/m")
	sub := filepath.Join(root, "internal")
	tests := []struct {
		dir  string
		args []string
		want []string
	}{
		{root, nil, []string{"./..."}},
		{sub, nil, []string{"./internal/..."}},
		{sub, []string{"."}, []string{"./internal"}},
		{sub, []string{"./store/...", "../cmd"}, []string{"./internal/store/...", "./cmd"}},
		{sub, []string{".."}, []string{"."}},
		{root, []string{"./cmd..."}, []string{"./cmd..."}},
		{sub, []string{"example.com/m/pkg/..."}, []string{"example.com/m/pkg/..."}},
		{root, []string{filepath.Join(root, "pkg") + "/..."}, []string{"./pkg/..."}},
	}
	for _, tt := range tests {
		got, err := rebase(tt.dir, root, tt.args)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("rebase(%s, %q) = %q, %v; want %q", tt.dir, tt.args, got, err, tt.want)
		}
	}
	if _, err := rebase(sub, root, []string{"../../other/..."}); err == nil {
		t.Error("a pattern outside the module was taken")
	}
}

func TestPickCommand(t *testing.T) {
	tests := []struct {
		args []string
		cmd  *command
		rest []string
		err  string
	}{
		{nil, auditCmd, nil, ""},
		{[]string{"-c", "4"}, auditCmd, []string{"-c", "4"}, ""},
		{[]string{"./..."}, auditCmd, []string{"./..."}, ""},
		{[]string{"example.com/m/..."}, auditCmd, []string{"example.com/m/..."}, ""},
		{[]string{"reports", "-n", "3"}, lookup("reports"), []string{"-n", "3"}, ""},
		{[]string{"config"}, lookup("config"), []string{}, ""},
		{[]string{"reprots"}, nil, nil, `unknown command "reprots"; packages are paths, such as ./reprots`},
	}
	for _, tt := range tests {
		cmd, rest, err := pickCommand(tt.args)
		if tt.err != "" {
			if err == nil || err.Error() != tt.err {
				t.Errorf("pickCommand(%q): err = %v, want %q", tt.args, err, tt.err)
			}
			continue
		}
		if err != nil || cmd != tt.cmd || !slices.Equal(rest, tt.rest) {
			t.Errorf("pickCommand(%q) = %v, %q, %v", tt.args, cmd.name, rest, err)
		}
	}
}

// snapshot returns the files under dir, path → content.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		files[path] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestAuditTools runs the built-in checks with the real tools over a module
// with a problem for each, and over it again with the tools out of PATH.
func TestAuditTools(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the tools")
	}
	for _, tool := range []string{"go", "gofmt", "gopls"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s in PATH", tool)
		}
	}
	sum, err := os.ReadFile("go.sum")
	if err != nil {
		t.Fatal(err)
	}
	var modSum []string
	for line := range strings.Lines(string(sum)) {
		if strings.HasPrefix(line, "golang.org/x/mod ") {
			modSum = append(modSum, line)
		}
	}
	root := t.TempDir()
	files := map[string]string{
		// golang.org/x/mod is required and not used: tidy.
		"go.mod": "module example.com/tools\n\ngo 1.26\n\nrequire golang.org/x/mod v0.42.0\n",
		"go.sum": strings.Join(modSum, ""),
		// interface{}: fix; a Scanner whose Err is never checked: gopls.
		"main.go": `package main

import (
	"bufio"
	"fmt"
	"os"
)

// Box holds anything.
type Box struct{ V interface{} }

func main() {
	s := bufio.NewScanner(os.Stdin)
	for s.Scan() {
		fmt.Println(s.Text(), Box{})
	}
}
`,
		// Not formatted: gofmt.
		"inner/inner.go": "package inner\n\nfunc  F( ) int { return 1 }\n",
	}
	for name, content := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(name)), content)
	}
	store := t.TempDir()
	t.Setenv("GO_STYLE_HOME", store)
	t.Setenv("GOPROXY", "off") // golang.org/x/mod comes from the module cache
	t.Setenv("GOFLAGS", "")
	t.Chdir(root)
	before := snapshot(t, root)

	out, err := auditOut(t, t.Context(), 2, "vet,fix,gopls,tidy,gofmt")
	if !errors.Is(err, exitStatus(1)) {
		t.Fatalf("audit of a module with problems: err = %v\n%s", err, out)
	}
	reports, err := loadReports(store)
	if err != nil || len(reports) != 1 {
		t.Fatalf("%d reports, %v", len(reports), err)
	}
	if want := "fix 1 · gopls 1 · tidy 1 · gofmt 1"; reports[0].Summary != want {
		t.Errorf("summary = %q, want %q\n%s", reports[0].Summary, want, out)
	}
	md, _ := os.ReadFile(reports[0].path)
	for _, want := range []string{
		"- `main.go:10:20` interface{} can be replaced by any (any)",
		"```go\n  type Box struct{ V interface{} }\n  ```",
		"- `main.go:13:7` bufio.Scanner \"s\" is used in Scan loop",
		"- `inner/inner.go`",
		"- diff current/go.mod tidy/go.mod",
	} {
		if !strings.Contains(string(md), want) {
			t.Errorf("report:\n%s\nwant it to contain %q", md, want)
		}
	}

	// Without the tools in PATH, their checks are skipped, with what to
	// install.
	t.Setenv("PATH", filepath.Dir(must(exec.LookPath("go"))))
	out, err = auditOut(t, t.Context(), 1, "nilaway,gostyle,gopls")
	if err != nil {
		t.Errorf("audit with the tools missing: err = %v\n%s", err, out)
	}
	for _, want := range []string{
		"  nilaway skipped     -      -  not installed: go install go.uber.org/nilaway/cmd/nilaway@latest",
		"  gostyle skipped     -      -  not installed: go install github.com/k1LoW/gostyle@latest",
		"  gopls   skipped     -      -  not installed: go install golang.org/x/tools/gopls@latest",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("audit with the tools missing:\n%s\nwant a line with %q", out, want)
		}
	}

	if after := snapshot(t, root); !maps.Equal(before, after) {
		t.Errorf("the audit changed the module: %d files before, %d after", len(before), len(after))
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseStat(t *testing.T) {
	// Command names may contain spaces and parentheses.
	line := "1234 (weird ) name) S 42 1234 1234 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 7 0 98765 1000000 500 18446744073709551615\n"
	st, err := parseStat(line)
	if err != nil {
		t.Fatal(err)
	}
	if st.ppid != 42 || st.startTime != "98765" {
		t.Fatalf("got ppid=%d start=%s, want 42 98765", st.ppid, st.startTime)
	}
	if _, err := parseStat("garbage"); err == nil {
		t.Fatal("expected error for malformed stat")
	}
}

// fakeEnv builds a fake sessions dir and procfs.
type fakeEnv struct {
	t         *testing.T
	dir, proc string
	marker    string
}

func newFakeEnv(t *testing.T) *fakeEnv {
	root := t.TempDir()
	e := &fakeEnv{t: t, dir: filepath.Join(root, "sessions"), proc: filepath.Join(root, "proc"),
		marker: "/home/u/.claude/shell-snapshots/"}
	for _, d := range []string{e.dir, e.proc} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *fakeEnv) scanner() Scanner {
	return Scanner{Dir: e.dir, Proc: e.proc, SnapshotMarker: e.marker}
}

func (e *fakeEnv) process(pid, ppid int, start string, args ...string) {
	d := filepath.Join(e.proc, fmt.Sprint(pid))
	must(e.t, os.MkdirAll(d, 0o755))
	stat := fmt.Sprintf("%d (x) S %d 0 0 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 %s 0 0\n", pid, ppid, start)
	must(e.t, os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644))
	must(e.t, os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.Join(args, "\x00")+"\x00"), 0o644))
}

func (e *fakeEnv) session(pid int, status, start string) {
	js := fmt.Sprintf(`{"pid":%d,"name":"s%d","cwd":"/w","status":%q,"procStart":%q,"extra":[1]}`, pid, pid, status, start)
	must(e.t, os.WriteFile(filepath.Join(e.dir, fmt.Sprintf("%d.json", pid)), []byte(js), 0o644))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorking(t *testing.T) {
	e := newFakeEnv(t)
	tool := "source " + e.marker + "snapshot-bash-1.sh; eval 'sleep 60'"

	e.process(10, 1, "100", "claude")
	e.session(10, "busy", "100") // working: busy

	e.process(11, 1, "100", "claude")
	e.session(11, "shell", "100") // working: running a command

	e.process(12, 1, "100", "claude")
	e.session(12, "idle", "100")
	e.process(120, 12, "101", "/bin/bash", "-c", tool) // working: background task

	e.process(13, 1, "100", "claude")
	e.session(13, "idle", "100")
	e.process(130, 13, "101", "node", "mcp-server")         // not a tool command
	e.process(131, 13, "101", "/bin/sh", "-c", "mcp-thing") // bash -c, but no snapshot

	e.session(14, "busy", "100") // stale: process gone

	e.process(15, 1, "999", "other")
	e.session(15, "busy", "100") // stale: pid reused by another process

	must(t, os.WriteFile(filepath.Join(e.dir, "16.json"), []byte("{not json"), 0o644))
	must(t, os.WriteFile(filepath.Join(e.dir, "16.key"), []byte("ignored"), 0o644))

	got := pids(t, e.scanner())
	if want := []int{10, 11, 12}; !slices.Equal(got, want) {
		t.Fatalf("working = %v, want %v", got, want)
	}

	s := e.scanner()
	s.SnapshotMarker = "" // background check disabled
	if got, want := pids(t, s), []int{10, 11}; !slices.Equal(got, want) {
		t.Fatalf("without background: working = %v, want %v", got, want)
	}
}

func TestMissingDir(t *testing.T) {
	s := Scanner{Dir: filepath.Join(t.TempDir(), "nope"), Proc: "/proc"}
	active, err := s.Working()
	if err != nil || len(active) != 0 {
		t.Fatalf("got %v, %v; want no sessions and no error", active, err)
	}
}

func pids(t *testing.T, s Scanner) []int {
	t.Helper()
	active, err := s.Working()
	must(t, err)
	var out []int
	for _, a := range active {
		out = append(out, a.PID)
	}
	slices.Sort(out)
	return out
}

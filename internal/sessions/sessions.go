// Package sessions finds running Claude Code sessions that are doing work.
//
// It reads ~/.claude/sessions/<pid>.json, which Claude Code writes for each
// running session. The format is undocumented, so this package is
// deliberately lenient: unreadable or unexpected files are skipped.
package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Reason says why a session counts as working.
type Reason string

const (
	// ReasonStatus: the session's own status is not idle (e.g. "busy", "shell").
	ReasonStatus Reason = "status"
	// ReasonBackground: the session is idle but has a Bash-tool child process
	// still running, i.e. a background task or Monitor the agent waits on.
	ReasonBackground Reason = "background"
)

// Active is a session that is currently working.
type Active struct {
	PID    int
	Name   string
	Cwd    string
	Status string
	Reason Reason
}

func (a Active) String() string {
	if a.Reason == ReasonBackground {
		return fmt.Sprintf("%s[%d] background task", a.Name, a.PID)
	}
	return fmt.Sprintf("%s[%d] %s", a.Name, a.PID, a.Status)
}

// Scanner looks for working sessions.
type Scanner struct {
	// Dir holds the session files, normally ~/.claude/sessions.
	Dir string
	// Proc is the procfs mount, normally /proc.
	Proc string
	// SnapshotMarker is a substring that identifies a Claude Code Bash-tool
	// command line. Every such command sources a shell snapshot from
	// ~/.claude/shell-snapshots/, so that path is used. Empty disables the
	// background-task check.
	SnapshotMarker string
}

// DefaultScanner returns a Scanner for the current user.
func DefaultScanner(background bool) (Scanner, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Scanner{}, err
	}
	s := Scanner{
		Dir:  filepath.Join(home, ".claude", "sessions"),
		Proc: "/proc",
	}
	if background {
		s.SnapshotMarker = filepath.Join(home, ".claude", "shell-snapshots") + "/"
	}
	return s, nil
}

type sessionFile struct {
	PID       int    `json:"pid"`
	Name      string `json:"name"`
	Cwd       string `json:"cwd"`
	Status    string `json:"status"`
	ProcStart string `json:"procStart"`
}

// Working returns the sessions that are currently working. A missing
// sessions directory is not an error: it just means there are no sessions.
func (s Scanner) Working() ([]Active, error) {
	paths, err := filepath.Glob(filepath.Join(s.Dir, "*.json"))
	if err != nil {
		return nil, err
	}

	var (
		active   []Active
		children map[int][]int // built lazily, only if an idle session needs it
	)
	for _, p := range paths {
		sf, ok := readSession(p)
		if !ok || !s.alive(sf) {
			continue
		}
		a := Active{PID: sf.PID, Name: sf.Name, Cwd: sf.Cwd, Status: sf.Status}
		if sf.Status != "idle" {
			a.Reason = ReasonStatus
			active = append(active, a)
			continue
		}
		if s.SnapshotMarker == "" {
			continue
		}
		if children == nil {
			children = s.childMap()
		}
		if s.hasToolChild(children[sf.PID]) {
			a.Reason = ReasonBackground
			active = append(active, a)
		}
	}
	return active, nil
}

func readSession(path string) (sessionFile, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return sessionFile{}, false
	}
	var sf sessionFile
	if json.Unmarshal(data, &sf) != nil || sf.PID <= 0 || sf.Status == "" {
		return sessionFile{}, false
	}
	return sf, true
}

// alive reports whether the session's process still runs. The start time is
// compared too, so a stale file whose pid was reused doesn't count.
func (s Scanner) alive(sf sessionFile) bool {
	st, err := readStat(s.Proc, sf.PID)
	if err != nil {
		return false
	}
	return sf.ProcStart == "" || st.startTime == sf.ProcStart
}

// hasToolChild reports whether any of the pids is a Claude Code Bash-tool
// command: `bash -c '<script sourcing a shell snapshot>'`.
func (s Scanner) hasToolChild(pids []int) bool {
	for _, pid := range pids {
		cmdline, err := os.ReadFile(filepath.Join(s.Proc, strconv.Itoa(pid), "cmdline"))
		if err != nil {
			continue
		}
		args := bytes.Split(bytes.TrimRight(cmdline, "\x00"), []byte{0})
		if len(args) >= 3 && string(args[1]) == "-c" &&
			bytes.Contains(args[2], []byte(s.SnapshotMarker)) {
			return true
		}
	}
	return false
}

// childMap maps each pid to its direct children, from one pass over procfs.
func (s Scanner) childMap() map[int][]int {
	m := make(map[int][]int)
	entries, err := os.ReadDir(s.Proc)
	if err != nil {
		return m
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, err := readStat(s.Proc, pid)
		if err != nil {
			continue
		}
		m[st.ppid] = append(m[st.ppid], pid)
	}
	return m
}

type stat struct {
	ppid      int
	startTime string
}

func readStat(proc string, pid int) (stat, error) {
	data, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "stat"))
	if err != nil {
		return stat{}, err
	}
	return parseStat(string(data))
}

var errBadStat = errors.New("malformed /proc stat")

// parseStat extracts ppid (field 4) and starttime (field 22) from the
// contents of /proc/<pid>/stat. The command name (field 2) is parenthesised
// and may contain spaces or parentheses, so parsing starts after the last ')'.
func parseStat(s string) (stat, error) {
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return stat{}, errBadStat
	}
	f := strings.Fields(s[i+1:]) // f[0] is field 3 (state)
	if len(f) < 20 {
		return stat{}, errBadStat
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return stat{}, errBadStat
	}
	return stat{ppid: ppid, startTime: f[19]}, nil
}

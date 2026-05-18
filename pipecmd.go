package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/mattn/go-runewidth"
)

// detectPipeCommand returns the command line feeding our stdin pipe.
//
// When invoked as `some-cmd 2>&1 | lm`, the shell places every member of the
// pipeline (including lm) in the same process group. We list all processes,
// keep the ones sharing our process group, drop ourselves and the `ps` child
// we just spawned, and join what's left in pid order. Works on Linux and macOS
// (both ship a `ps` that understands `-o pgid=,pid=,ppid=,command=`).
func detectPipeCommand() string {
	me := os.Getpid()
	pgid := syscall.Getpgrp()

	out, err := exec.Command("ps", "-A", "-ww", "-o", "pgid=,pid=,ppid=,command=").Output()
	if err != nil {
		return ""
	}

	type proc struct {
		pid int
		cmd string
	}
	var members []proc

	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		pg, e1 := strconv.Atoi(fields[0])
		pid, e2 := strconv.Atoi(fields[1])
		ppid, e3 := strconv.Atoi(fields[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		if pg != pgid || pid == me || ppid == me {
			continue // not our pipeline, or it's us / the ps we spawned
		}
		cmd := sanitizeCmd(strings.Join(fields[3:], " "))
		if cmd == "" || strings.HasPrefix(cmd, "ps -A -ww") {
			continue
		}
		members = append(members, proc{pid, cmd})
	}

	if len(members) == 0 {
		return ""
	}
	sort.Slice(members, func(i, j int) bool { return members[i].pid < members[j].pid })
	parts := make([]string, len(members))
	for i, p := range members {
		parts[i] = p.cmd
	}
	return strings.Join(parts, " | ")
}

// sanitizeCmd collapses any newlines/tabs/runs of whitespace into single
// spaces so the command renders on one status-bar line.
func sanitizeCmd(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncCmd trims s to a maximum visible width, appending "…" when cut.
func truncCmd(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > max-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	b.WriteRune('…')
	return b.String()
}

package main

import (
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// listListeners uses lsof, which ships with macOS.
func listListeners() ([]Listener, error) {
	out, err := exec.Command("lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-F", "pgcn").Output()
	if err != nil && len(out) == 0 {
		return nil, nil // lsof exits 1 when nothing matches
	}
	var ls []Listener
	var pid, pgid int
	var comm string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		v := line[1:]
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(v)
		case 'g':
			pgid, _ = strconv.Atoi(v)
		case 'c':
			comm = v
		case 'n':
			i := strings.LastIndexByte(v, ':')
			if i < 0 {
				continue
			}
			port, err := strconv.Atoi(v[i+1:])
			if err != nil {
				continue
			}
			host := strings.Trim(v[:i], "[]")
			if host == "*" {
				host = "0.0.0.0"
			}
			ip := net.ParseIP(host)
			if ip == nil || !(ip.IsLoopback() || ip.IsUnspecified()) {
				continue
			}
			ls = append(ls, Listener{Port: port, Addr: ip.String(), PID: pid, PGID: pgid, Process: comm})
		}
	}
	ls = dedupeListeners(ls)
	addProcInfo(ls)
	return ls, nil
}

// addProcInfo fills in each listener's working directory (via lsof) and command line (via ps).
// Failures are ignored: the fields are only hints for naming.
func addProcInfo(ls []Listener) {
	if len(ls) == 0 {
		return
	}
	seen := map[int]bool{}
	var pids []string
	for _, l := range ls {
		if l.PID > 0 && !seen[l.PID] {
			seen[l.PID] = true
			pids = append(pids, strconv.Itoa(l.PID))
		}
	}
	list := strings.Join(pids, ",")
	cwd, cmd := map[int]string{}, map[int]string{}
	out, _ := exec.Command("lsof", "-a", "-p", list, "-d", "cwd", "-Fn").Output()
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			cwd[pid] = line[1:]
		}
	}
	out, _ = exec.Command("ps", "-ww", "-o", "pid=,command=", "-p", list).Output()
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.SplitN(strings.TrimSpace(line), " ", 2)
		if len(f) == 2 {
			if p, err := strconv.Atoi(f[0]); err == nil {
				cmd[p] = strings.TrimSpace(f[1])
			}
		}
	}
	for i := range ls {
		ls[i].Cwd, ls[i].Command = cwd[ls[i].PID], cmd[ls[i].PID]
	}
}

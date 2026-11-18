package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Listener is a TCP socket listening on a loopback or wildcard address.
type Listener struct {
	Port         int    `json:"port"`
	Addr         string `json:"addr"`
	PID          int    `json:"pid,omitempty"`
	PGID         int    `json:"-"`
	Process      string `json:"process,omitempty"`
	Command      string `json:"command,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	Project      string `json:"project,omitempty"` // enclosing project directory, if any
	HTTP         bool   `json:"http"`
	RegisteredAs string `json:"registered_as,omitempty"`
}

func dedupeListeners(ls []Listener) []Listener {
	seen := map[[2]int]bool{}
	var out []Listener
	for _, l := range ls {
		k := [2]int{l.Port, l.PID}
		if !seen[k] {
			seen[k] = true
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// hiddenListener reports whether discovery should never list a listener: Claude Code
// sessions (each opens a local port) and other localdev daemons.
func hiddenListener(l Listener) bool {
	if l.Process == "localdev" {
		return true
	}
	f := strings.Fields(l.Command)
	if len(f) == 0 {
		return false
	}
	exe := filepath.Base(f[0])
	return exe == "claude" || exe == "localdev" || strings.Contains(f[0], "/claude/versions/")
}

// projectMarkers are files or directories that mark the root of a software project.
var projectMarkers = []string{
	".git", ".hg", "package.json", "deno.json", "pyproject.toml", "setup.py", "requirements.txt",
	"Pipfile", "go.mod", "Cargo.toml", "Gemfile", "composer.json", "pom.xml", "build.gradle",
	"build.gradle.kts", "mix.exs", "pubspec.yaml", "Package.swift",
}

// projectRoot returns the nearest directory at or above the listener's working directory
// that contains a project marker. Without a marker, a known dev-server tool (see devTool) makes
// its working directory the project; otherwise it returns "". The search stops below the home
// directory and the filesystem root. System and desktop-app services never belong to a project.
func projectRoot(l Listener) string {
	if l.Cwd == "" || l.Cwd == "/" || strings.Contains(l.Cwd, ".app/") ||
		strings.HasPrefix(l.Command, "/System/") || strings.HasPrefix(l.Command, "/Applications/") {
		return ""
	}
	home, _ := os.UserHomeDir()
	for dir := filepath.Clean(l.Cwd); dir != "/" && dir != home && dir != "."; dir = filepath.Dir(dir) {
		for _, m := range projectMarkers {
			if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
				return dir
			}
		}
	}
	if devTool(l.Command) {
		return filepath.Clean(l.Cwd)
	}
	return ""
}

// devTools are commands that serve a project during development (notebooks, app frameworks,
// bundlers, ASGI/WSGI servers, static site generators).
var devTools = map[string]bool{
	"marimo": true, "jupyter": true, "jupyter-lab": true, "jupyter-notebook": true,
	"jupyterlab": true, "voila": true, "streamlit": true, "gradio": true, "panel": true,
	"bokeh": true, "shiny": true, "dash": true, "uvicorn": true, "gunicorn": true,
	"hypercorn": true, "granian": true, "fastapi": true, "flask": true, "django-admin": true,
	"manage.py": true, "mkdocs": true, "sphinx-autobuild": true, "quarto": true,
	"vite": true, "next": true, "nuxt": true, "astro": true, "remix": true,
	"webpack": true, "webpack-dev-server": true, "parcel": true, "ng": true, "vue-cli-service": true,
	"react-scripts": true, "svelte-kit": true, "docusaurus": true, "eleventy": true,
	"storybook": true, "hugo": true, "jekyll": true, "zola": true, "rails": true,
	"air": true, "wrangler": true, "netlify": true, "vercel": true,
}

// devTool reports whether a command line runs a known dev-server tool, directly
// ("marimo run x.py"), through an interpreter or runner ("python .../bin/marimo",
// "uv run streamlit", "npx vite"), or as a module ("python -m uvicorn").
func devTool(command string) bool {
	f := strings.Fields(command)
	for i := 0; i < len(f) && i < 4; i++ {
		a := f[i]
		if a == "-m" && i+1 < len(f) {
			return devTools[strings.SplitN(f[i+1], ".", 2)[0]]
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		if devTools[strings.ToLower(filepath.Base(a))] {
			return true
		}
	}
	return false
}

// discover scans listeners on unprivileged ports and probes whether they speak HTTP.
func discover(skipPorts map[int]bool, registered map[int]string) ([]Listener, error) {
	ls, err := listListeners()
	if err != nil {
		return nil, err
	}
	var out []Listener
	for _, l := range ls {
		if l.Port >= 1024 && !skipPorts[l.Port] && !hiddenListener(l) {
			l.RegisteredAs = registered[l.Port]
			l.Project = projectRoot(l)
			out = append(out, l)
		}
	}
	client := &http.Client{
		Timeout:       700 * time.Millisecond,
		Transport:     &http.Transport{Proxy: nil, DialContext: dialLoopback, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func(l *Listener) {
			defer wg.Done()
			resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", l.Port))
			if err == nil {
				resp.Body.Close()
				l.HTTP = true
			}
		}(&out[i])
	}
	wg.Wait()
	return out, nil
}

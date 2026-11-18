package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSuggestName(t *testing.T) {
	cases := []struct {
		l    Listener
		want string
	}{
		{Listener{Cwd: "/home/u/My Shop", Process: "node"}, "my-shop"},
		{Listener{Cwd: "/", Process: "Python3.12"}, "python3-12"},
		{Listener{Process: "vite"}, "vite"},
		{Listener{Cwd: "/x/___", Process: "!!"}, "app"},
	}
	for _, c := range cases {
		if got := suggestName(c.l); got != c.want {
			t.Errorf("suggestName(%+v) = %q, want %q", c.l, got, c.want)
		}
	}
}

func TestAutoRegister(t *testing.T) {
	d := &Daemon{paths: paths{config: t.TempDir(), state: t.TempDir()}, port: 80, apps: map[string]*App{}, dismissed: map[int]int{}}
	d.apps["web"] = &App{cfg: &AppConfig{Name: "web", Port: 3000}}

	ls := []Listener{
		{Port: 3000, PID: 1, Cwd: "/p/web", HTTP: true, RegisteredAs: "web"}, // already registered
		{Port: 5173, PID: 2, Cwd: "/p/web", Project: "/p/web", HTTP: true},   // name clash -> web-5173
		{Port: 8000, PID: 3, Process: "python3", Cwd: "/srv/x y", Project: "/srv", HTTP: true},
		{Port: 9000, PID: 5, Process: "AirServer", Cwd: "/", HTTP: true}, // no project
		{Port: 5432, PID: 4, Process: "postgres"},                        // not HTTP
	}
	got := d.autoRegisterLocked(ls)
	if want := []string{"web-5173", "x-y"}; !slices.Equal(got, want) {
		t.Fatalf("added %v, want %v", got, want)
	}
	if d.apps["web-5173"].cfg.Port != 5173 || d.apps["web-5173"].cfg.Command != "" {
		t.Errorf("web-5173 not registered as proxy-only app on 5173: %+v", d.apps["web-5173"].cfg)
	}
	if ls[2].RegisteredAs != "x-y" {
		t.Errorf("listener not marked registered: %+v", ls[2])
	}
	cfgs, err := loadRegistry(d.paths)
	if err != nil || len(cfgs) != 3 {
		t.Fatalf("registry not saved: %v %v", cfgs, err)
	}

	// Unregistering keeps the same process from being re-added; a new process is added.
	d.discovered = ls
	delete(d.apps, "x-y")
	d.dismissLocked(8000)
	again := []Listener{{Port: 8000, PID: 3, Process: "python3", Cwd: "/srv/x y", Project: "/srv", HTTP: true}}
	if got := d.autoRegisterLocked(again); len(got) != 0 {
		t.Errorf("dismissed app re-added: %v", got)
	}
	again[0].PID = 9
	if got := d.autoRegisterLocked(again); !slices.Equal(got, []string{"x-y"}) {
		t.Errorf("new process on dismissed port: added %v", got)
	}
}

func TestAutoEligibleIgnore(t *testing.T) {
	d := &Daemon{autoIgnore: splitList(" Foo ,bar")}
	cases := []struct {
		l    Listener
		want bool
	}{
		{Listener{Project: "/p", Command: "node server.js", Process: "node"}, true},
		{Listener{Project: "", Command: "node server.js", Process: "node"}, false},
		{Listener{Project: "/p", Command: "/usr/bin/foo --x", Process: "x"}, false},
		{Listener{Project: "/p", Command: "baz", Process: "BAR"}, false},
	}
	for _, c := range cases {
		if got := d.autoEligible(c.l); got != c.want {
			t.Errorf("autoEligible(%+v) = %v, want %v", c.l, got, c.want)
		}
	}
}

func TestProjectRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mk := func(rel string, marker string) string {
		dir := filepath.Join(home, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if marker != "" {
			if err := os.WriteFile(filepath.Join(dir, marker), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	os.WriteFile(filepath.Join(home, "package.json"), nil, 0o644) // markers in $HOME itself don't count
	repo := mk("work/shop", "go.mod")
	sub := mk("work/shop/web/src", "")
	loose := mk("Music/iphone_sync", "")
	app := mk("Library/Foo.app/Contents/Frameworks", "package.json")

	cases := []struct {
		l    Listener
		want string
	}{
		{Listener{Cwd: repo, Command: "go run ."}, repo},
		{Listener{Cwd: sub, Command: "npm run dev"}, repo},
		{Listener{Cwd: loose, Command: "python3 sync_app.py"}, ""},
		{Listener{Cwd: home, Command: "node x.js"}, ""},
		{Listener{Cwd: app, Command: app + "/../MacOS/Foo"}, ""},
		{Listener{Cwd: repo, Command: "/Applications/AirServer.app/Contents/MacOS/AirServer"}, ""},
		{Listener{Cwd: "/", Command: "/System/Library/CoreServices/ControlCenter.app/Contents/MacOS/ControlCenter"}, ""},
		{Listener{Cwd: "", Command: "node"}, ""},
		{Listener{Cwd: loose, Command: "/u/.cache/uv/x/bin/python /u/.cache/uv/x/bin/marimo run nb.py"}, loose},
		{Listener{Cwd: loose, Command: "python3 -m http.server 8000"}, ""},
	}
	for _, c := range cases {
		if got := projectRoot(c.l); got != c.want {
			t.Errorf("projectRoot(%+v) = %q, want %q", c.l, got, c.want)
		}
	}
}

func TestHiddenListener(t *testing.T) {
	cases := map[string]bool{
		"claude":          true,
		"claude --resume": true,
		"/Users/u/.local/share/claude/versions/2.1.327": true,
		"/usr/local/bin/claude -p hi":                   true,
		"./localdev daemon run --auto-register":         true,
		"node src/main.ts":                              false,
		"python3 claude_tools.py":                       false,
		"":                                              false,
	}
	for cmd, want := range cases {
		if got := hiddenListener(Listener{Command: cmd}); got != want {
			t.Errorf("hiddenListener(%q) = %v, want %v", cmd, got, want)
		}
	}
	if !hiddenListener(Listener{Process: "localdev"}) {
		t.Error("localdev process not hidden")
	}
}

func TestDevTool(t *testing.T) {
	cases := map[string]bool{
		"marimo edit nb.py": true,
		"/h/.cache/uv/builds-v0/.tmp/bin/python /h/.cache/uv/builds-v0/.tmp/bin/marimo run nb.py": true,
		"python -m uvicorn app:app --reload":                                                      true,
		"uv run streamlit run app.py":                                                             true,
		"node /p/node_modules/.bin/vite --port 5173":                                              true,
		"npx next dev":                true,
		"python manage.py runserver":  true,
		"python3 sync_app.py":         false,
		"python3 -m http.server 8000": false,
		"node src/main.ts":            false,
		"/Applications/AirServer.app/Contents/MacOS/AirServer": false,
		"": false,
	}
	for cmd, want := range cases {
		if got := devTool(cmd); got != want {
			t.Errorf("devTool(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestPruneAutoRegistered(t *testing.T) {
	d := &Daemon{paths: paths{config: t.TempDir(), state: t.TempDir()}, port: 80, apps: map[string]*App{},
		dismissed: map[int]int{}, autoRemove: 2 * time.Minute, lastSeen: map[string]time.Time{}}
	d.apps["mine"] = &App{cfg: &AppConfig{Name: "mine", Port: 1000}} // registered by hand
	added := d.autoRegisterLocked([]Listener{{Port: 5173, PID: 1, Cwd: "/p/decks", Project: "/p/decks", HTTP: true}})
	if !slices.Equal(added, []string{"decks"}) || !d.apps["decks"].cfg.AutoRegistered {
		t.Fatalf("auto-register: added %v, cfg %+v", added, d.apps["decks"].cfg)
	}

	t0 := time.Now()
	up := []Listener{{Port: 5173}}
	if got := d.pruneLocked(up, t0); got != nil {
		t.Fatalf("pruned while listening: %v", got)
	}
	if got := d.pruneLocked(nil, t0.Add(90*time.Second)); got != nil {
		t.Fatalf("pruned within grace period: %v", got)
	}
	if got := d.pruneLocked(up, t0.Add(100*time.Second)); got != nil {
		t.Fatalf("pruned after coming back: %v", got)
	}
	if got := d.pruneLocked(nil, t0.Add(4*time.Minute)); !slices.Equal(got, []string{"decks"}) {
		t.Fatalf("pruned %v, want [decks]", got)
	}
	if d.apps["decks"] != nil || d.apps["mine"] == nil {
		t.Errorf("apps after prune: %v", d.apps)
	}
	if cfgs, _ := loadRegistry(d.paths); cfgs["decks"] != nil {
		t.Error("pruned app still in registry")
	}

	// An app first seen closed gets the full grace period from the daemon's first look.
	d.apps["late"] = &App{cfg: &AppConfig{Name: "late", Port: 7000, AutoRegistered: true}}
	if got := d.pruneLocked(nil, t0.Add(10*time.Minute)); got != nil {
		t.Errorf("pruned on first sight: %v", got)
	}
	// Disabled with 0.
	d.autoRemove = 0
	if got := d.pruneLocked(nil, t0.Add(time.Hour)); got != nil {
		t.Errorf("pruned with auto-remove off: %v", got)
	}
}

func TestAddDevTools(t *testing.T) {
	cmd := "python3 /Users/x/.local/bin/shakels"
	if devTool(cmd) {
		t.Fatalf("devTool(%q) = true before adding", cmd)
	}
	addDevTools(" Shakels , ,other")
	t.Cleanup(func() { delete(devTools, "shakels"); delete(devTools, "other") })
	if !devTool(cmd) {
		t.Errorf("devTool(%q) = false after addDevTools", cmd)
	}
}

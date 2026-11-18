package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRename(t *testing.T) {
	d := &Daemon{paths: paths{config: t.TempDir(), state: t.TempDir()}, port: 80, apps: map[string]*App{}, dismissed: map[int]int{}}
	d.apps["old"] = &App{cfg: &AppConfig{Name: "old", Port: 1, AutoRegistered: true}}
	d.apps["taken"] = &App{cfg: &AppConfig{Name: "taken", Port: 2}}
	os.MkdirAll(d.paths.state+"/logs", 0o755)
	os.WriteFile(d.paths.logFile("old"), []byte("hi"), 0o644)

	rename := func(from, body string) int {
		r := httptest.NewRequest("POST", "/api/apps/"+from+"/rename", strings.NewReader(body))
		r.SetPathValue("name", from)
		w := httptest.NewRecorder()
		d.handleRename(w, r)
		return w.Code
	}
	if c := rename("old", `{"name":"Bad Name"}`); c != 400 {
		t.Errorf("invalid name: got %d, want 400", c)
	}
	if c := rename("nope", `{"name":"x"}`); c != 404 {
		t.Errorf("missing app: got %d, want 404", c)
	}
	if c := rename("old", `{"name":"taken"}`); c != 409 {
		t.Errorf("name taken: got %d, want 409", c)
	}
	if c := rename("old", `{"name":"fresh"}`); c != 200 {
		t.Fatalf("rename: got %d, want 200", c)
	}
	if d.apps["old"] != nil || d.apps["fresh"] == nil || d.apps["fresh"].cfg.Name != "fresh" || d.apps["fresh"].cfg.Port != 1 {
		t.Errorf("apps after rename: %v", d.apps)
	}
	if d.apps["fresh"].cfg.AutoRegistered {
		t.Error("renamed app still marked auto-registered")
	}
	if b, err := os.ReadFile(d.paths.logFile("fresh")); err != nil || string(b) != "hi" {
		t.Errorf("log not moved: %q %v", b, err)
	}
	cfgs, err := loadRegistry(d.paths)
	if err != nil || cfgs["fresh"] == nil || cfgs["old"] != nil {
		t.Errorf("registry not saved: %v %v", cfgs, err)
	}

	// A running managed app must be stopped first.
	d.apps["svc"] = &App{cfg: &AppConfig{Name: "svc", Port: 3, Command: "x"}, proc: &Proc{done: make(chan struct{})}}
	if c := rename("svc", `{"name":"svc2"}`); c != 409 {
		t.Errorf("running app: got %d, want 409", c)
	}
}

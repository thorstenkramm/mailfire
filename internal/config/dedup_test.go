package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestDedupDefaults: the state directory defaults to the "dedup"
// subdirectory of the platform data directory, independent of a
// configured defer_dir, and the cooldown to one hour.
func TestDedupDefaults(t *testing.T) {
	home, _ := isolate(t)
	path := writeConfig(t, t.TempDir(), minimalTOML+"\n[defer]\ndefer = false\ndefer_dir = '"+filepath.Join(home, "q")+"'\n")
	cfg := loadOK(t, path)
	want := filepath.Join(home, ".local", "share", "mailfire", "dedup")
	if runtime.GOOS == "windows" {
		want = filepath.Join(home, "local", "mailfire", "dedup")
	}
	if cfg.DedupDir != want {
		t.Errorf("DedupDir = %q, want %q", cfg.DedupDir, want)
	}
	if cfg.DedupCooldown != time.Hour {
		t.Errorf("DedupCooldown = %v, want 1h", cfg.DedupCooldown)
	}
	if err := cfg.CheckDedupDir(); err != nil {
		t.Errorf("CheckDedupDir: %v", err)
	}
}

func TestDedupPlatformDefaults(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	path := writeConfig(t, t.TempDir(), minimalTOML)
	cfg, err := load(path, nil, "windows", env(map[string]string{"LOCALAPPDATA": "/local"}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/local", "mailfire", "dedup"); cfg.DedupDir != want {
		t.Errorf("windows DedupDir = %q, want %q", cfg.DedupDir, want)
	}
	cfg, err = load(path, nil, "linux", env(map[string]string{"HOME": "/home/u"}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/u", ".local", "share", "mailfire", "dedup"); cfg.DedupDir != want {
		t.Errorf("unix DedupDir = %q, want %q", cfg.DedupDir, want)
	}
}

func TestDedupSettings(t *testing.T) {
	isolate(t)
	path := writeConfig(t, t.TempDir(), minimalTOML+"\n[dedup]\nstate_dir = \"~/alerts\"\ncooldown = \"30m\"\n")
	cfg := loadOK(t, path)
	if filepath.Base(cfg.DedupDir) != "alerts" || strings.HasPrefix(cfg.DedupDir, "~") {
		t.Errorf("DedupDir = %q, want ~ expanded", cfg.DedupDir)
	}
	if cfg.DedupCooldown != 30*time.Minute {
		t.Errorf("DedupCooldown = %v, want 30m", cfg.DedupCooldown)
	}
}

func TestDedupInvalidCooldown(t *testing.T) {
	isolate(t)
	for _, v := range []string{"soon", "0s", "-1m", " "} {
		path := writeConfig(t, t.TempDir(), minimalTOML+"\n[dedup]\ncooldown = \""+v+"\"\n")
		if strings.TrimSpace(v) == "" {
			loadOK(t, path) // empty means default
			continue
		}
		if msg := loadErr(t, path); !strings.Contains(msg, "dedup.cooldown") {
			t.Errorf("cooldown %q: error %q does not name dedup.cooldown", v, msg)
		}
	}
	t.Setenv("MAILFIRE_DEDUP_COOLDOWN", "1x")
	path := writeConfig(t, t.TempDir(), minimalTOML)
	if msg := loadErr(t, path); !strings.Contains(msg, "MAILFIRE_DEDUP_COOLDOWN") {
		t.Errorf("error %q does not name MAILFIRE_DEDUP_COOLDOWN", msg)
	}
}

// TestCheckDedupDir: the state directory must not sit inside the
// defer_dir where the deferred-message cleanup would remove it, except in
// the reserved "dedup" subdirectory.
func TestCheckDedupDir(t *testing.T) {
	base := t.TempDir()
	q := filepath.Join(base, "q")
	for _, tc := range []struct {
		dedupDir string
		ok       bool
	}{
		{filepath.Join(q, "dedup"), true},
		{q, true},
		{filepath.Join(base, "elsewhere"), true},
		{base, true},
		{filepath.Join(base, "q-dedup"), true},
		{filepath.Join(q, "state"), false},
		{filepath.Join(q, "dedup", "deeper"), false},
	} {
		cfg := &Config{DeferDir: q, DedupDir: tc.dedupDir}
		err := cfg.CheckDedupDir()
		if (err == nil) != tc.ok {
			t.Errorf("state_dir %s: err = %v, want ok=%v", tc.dedupDir, err, tc.ok)
		}
	}
	if err := (&Config{}).CheckDedupDir(); err == nil || !strings.Contains(err.Error(), "state_dir") {
		t.Errorf("no state_dir: err = %v", err)
	}
	if err := (&Config{DedupDir: q}).CheckDedupDir(); err != nil {
		t.Errorf("no defer_dir: err = %v", err)
	}
}

// Symbolic links cannot alias the state directory into an unsafe place
// inside the defer_dir, through the state path or the defer_dir path.
func TestCheckDedupDirSymlinks(t *testing.T) {
	base := t.TempDir()
	q := filepath.Join(base, "q")
	if err := os.MkdirAll(filepath.Join(q, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := func(target, name string) string {
		t.Helper()
		p := filepath.Join(base, name)
		if err := os.Symlink(target, p); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		return p
	}
	alias := link(filepath.Join(q, "inner"), "alias") // outside name, inside target
	qAlias := link(q, "q-alias")                      // the defer_dir under another name
	dangling := link(filepath.Join(base, "missing"), "dangling")
	if err := os.Symlink(filepath.Join(q, "inner"), filepath.Join(q, "dedup")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		deferDir, dedupDir string
		ok                 bool
	}{
		{q, alias, false}, // existing alias into the queue
		{q, filepath.Join(alias, "not-yet"), false},      // not yet created below an alias
		{qAlias, filepath.Join(q, "state"), false},       // defer_dir given through an alias
		{q, filepath.Join(qAlias, "state"), false},       // state given through an alias
		{q, filepath.Join(q, "dedup"), false},            // reserved name linked to another subdirectory
		{qAlias, filepath.Join(base, "elsewhere"), true}, // genuinely outside
		{q, filepath.Join(dangling, "state"), true},
	} {
		err := (&Config{DeferDir: tc.deferDir, DedupDir: tc.dedupDir}).CheckDedupDir()
		if (err == nil) != tc.ok {
			t.Errorf("defer_dir %s, state_dir %s: err = %v, want ok=%v", tc.deferDir, tc.dedupDir, err, tc.ok)
		}
	}
}

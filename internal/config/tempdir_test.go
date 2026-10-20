package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTempDirConfigAndCheck(t *testing.T) {
	home, _ := isolate(t)
	parent := filepath.Join(home, "cache")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, t.TempDir(), minimalTOML+`temp_dir = "~/cache/mailfire"
`)
	cfg := loadOK(t, path)
	want := filepath.Join(home, "cache", "mailfire")
	if cfg.TempDir != want {
		t.Fatalf("TempDir = %q, want %q", cfg.TempDir, want)
	}
	if err := cfg.CheckTempDir(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Errorf("created temp_dir = %v, want directory", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("created temp_dir = %v, want owner-only directory", info.Mode())
	}

	t.Run("missing parent", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing", "temp")
		err := (&Config{TempDir: missing}).CheckTempDir()
		if err == nil || !strings.Contains(err.Error(), "parent directory") {
			t.Fatalf("CheckTempDir = %v, want missing-parent error", err)
		}
		if _, statErr := os.Stat(filepath.Dir(missing)); !os.IsNotExist(statErr) {
			t.Errorf("parent was created: %v", statErr)
		}
	})

	t.Run("file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		err := (&Config{TempDir: file}).CheckTempDir()
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("CheckTempDir = %v, want not-a-directory error", err)
		}
	})
}

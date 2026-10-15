package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// searchPaths returns the config file locations for the given platform in
// search order; the first existing file wins. See PRD FR-2.
func searchPaths(goos string, getenv func(string) string) []string {
	if goos == "windows" {
		return []string{
			filepath.Join(".", ".mailfire.conf"),
			filepath.Join(getenv("APPDATA"), "mailfire", "mailfire.conf"),
			filepath.Join(getenv("PROGRAMDATA"), "mailfire", "mailfire.conf"),
		}
	}
	return []string{
		filepath.Join(".", ".mailfire.conf"),
		filepath.Join(getenv("HOME"), ".config", "mailfire", "mailfire.conf"),
		filepath.Join("/etc", "mailfire", "mailfire.conf"),
	}
}

// defaultDeferDir returns the per-platform default directory for deferred
// messages: $HOME/.local/share/mailfire on Unix, %LOCALAPPDATA%\mailfire on
// Windows.
func defaultDeferDir(goos string, getenv func(string) string) string {
	if goos == "windows" {
		if d := getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "mailfire")
		}
		return filepath.Join(getenv("APPDATA"), "mailfire")
	}
	return filepath.Join(getenv("HOME"), ".local", "share", "mailfire")
}

// expandPath replaces a leading ~ (or ~/ or ~\) with the user's home
// directory. Paths without a tilde prefix are returned unchanged.
func expandPath(p string, getenv func(string) string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p
	}
	home := getenv("HOME")
	if home == "" {
		home = getenv("USERPROFILE") // Windows
	}
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return home + p[1:]
}

// warnIfPermissive writes a warning to stderr when the config file is
// readable or writable by group/others (permissions more permissive than
// 0600). The check is skipped on Windows and when stderr is nil.
func warnIfPermissive(goos, path string, stderr io.Writer) {
	if goos == "windows" || stderr == nil {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		fmt.Fprintf(stderr, "mailfire: warning: config file %s has permissions %04o; it contains your API key, consider: chmod 600 %s\n", path, perm, path)
	}
}

package config

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// searchPaths returns the config file locations for the given platform in
// search order; the first existing file wins. See PRD FR-2. Locations whose
// base directory comes from an unset environment variable are omitted, so
// they cannot silently turn into paths relative to the working directory.
func searchPaths(goos string, getenv func(string) string) []string {
	paths := []string{filepath.Join(".", ".mailfire.conf")}
	if goos == "windows" {
		for _, env := range []string{"APPDATA", "PROGRAMDATA"} {
			if d := getenv(env); d != "" {
				paths = append(paths, filepath.Join(d, "mailfire", "mailfire.conf"))
			}
		}
		return paths
	}
	if home := getenv("HOME"); home != "" {
		paths = append(paths, filepath.Join(home, ".config", "mailfire", "mailfire.conf"))
	}
	return append(paths, filepath.Join("/etc", "mailfire", "mailfire.conf"))
}

// defaultDeferDir returns the per-platform default directory for deferred
// messages: $HOME/.local/share/mailfire on Unix, %LOCALAPPDATA%\mailfire on
// Windows. It returns "" when the base directory is unknown.
func defaultDeferDir(goos string, getenv func(string) string) string {
	if goos == "windows" {
		for _, env := range []string{"LOCALAPPDATA", "APPDATA"} {
			if d := getenv(env); d != "" {
				return filepath.Join(d, "mailfire")
			}
		}
		return ""
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "share", "mailfire")
	}
	return ""
}

// ExpandPath replaces a leading ~ (or ~/ or ~\) with the user's home
// directory, using the process environment. Paths without a tilde prefix
// are returned unchanged.
func ExpandPath(p string) string { return expandPath(p, os.Getenv) }

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
	return filepath.Join(home, p[2:])
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
		if _, err := fmt.Fprintf(stderr, "mailfire: warning: config file %s has permissions %04o; it contains your API key, consider: chmod 600 %s\n", path, perm, path); err != nil {
			log.Printf("mailfire: writing permission warning: %v", err)
		}
	}
}

package testutil

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// ClearMailfireEnv unsets every inherited MAILFIRE_* variable, so the
// developer's shell environment cannot change which configuration tests
// load. Call it from TestMain before m.Run and abort on error: a
// variable that could not be cleared would leak into the tests.
func ClearMailfireEnv() error {
	var errs []error
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); isMailfireVar(name, runtime.GOOS) {
			if err := os.Unsetenv(name); err != nil {
				errs = append(errs, fmt.Errorf("testutil: unsetting %s: %w", name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// isMailfireVar reports whether name is a MAILFIRE_* variable as the
// platform sees it: Windows variable names are case-insensitive, so an
// inherited mailfire_mailgun_api_key also configures Mailfire there.
func isMailfireVar(name, goos string) bool {
	if goos == "windows" {
		name = strings.ToUpper(name)
	}
	return strings.HasPrefix(name, "MAILFIRE_")
}

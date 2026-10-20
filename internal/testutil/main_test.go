package testutil

import (
	"os"
	"testing"
)

// The tests here send through the Mailgun client, which leaves the message
// spool to its caller.
func TestMain(m *testing.M) { os.Exit(RunWithPrivateTempDir(m)) }

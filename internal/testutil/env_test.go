package testutil

import (
	"os"
	"testing"
)

func TestIsMailfireVar(t *testing.T) {
	for _, tc := range []struct {
		name, goos string
		want       bool
	}{
		{"MAILFIRE_MAILGUN_API_KEY", "linux", true},
		{"MAILFIRE_MAILGUN_API_KEY", "windows", true},
		{"mailfire_mailgun_api_key", "windows", true},
		{"Mailfire_Sending_From", "windows", true},
		{"mailfire_mailgun_api_key", "linux", false}, // a distinct variable on Unix
		{"mailfire_mailgun_api_key", "darwin", false},
		{"MAILFIREX", "windows", false},
		{"HOME", "linux", false},
	} {
		if got := isMailfireVar(tc.name, tc.goos); got != tc.want {
			t.Errorf("isMailfireVar(%q, %q) = %v, want %v", tc.name, tc.goos, got, tc.want)
		}
	}
}

func TestClearMailfireEnv(t *testing.T) {
	t.Setenv("MAILFIRE_SENDING_DOMAIN", "example.com")
	t.Setenv("NOT_MAILFIRE_X", "keep")
	if err := ClearMailfireEnv(); err != nil {
		t.Fatal(err)
	}
	if _, ok := os.LookupEnv("MAILFIRE_SENDING_DOMAIN"); ok {
		t.Error("MAILFIRE_SENDING_DOMAIN still set")
	}
	if os.Getenv("NOT_MAILFIRE_X") != "keep" {
		t.Error("unrelated variable cleared")
	}
}

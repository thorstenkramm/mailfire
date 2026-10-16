package cli

import (
	"bytes"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

type brokenOutput struct{}

func (brokenOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// A broken pipe after delivery must not ask the caller to retry or leave
// a successfully resent message queued for another delivery.
func TestDeliveredWithBrokenOutput(t *testing.T) {
	var diagnostics bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&diagnostics)
	t.Cleanup(func() { log.SetOutput(previous) })
	for _, resend := range []bool{false, true} {
		t.Run(map[bool]string{false: "send", true: "resend"}[resend], func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			cfg, dir := deferConfig(t, "")
			args := []string{"send", "-c", cfg, "--to", "a@example.com", "--subject", "s"}
			if resend {
				seedDeferred(t, dir, "a@example.com", "s", time.Hour)
				args = []string{"resend", "-c", cfg, "--force"}
			}
			code := Run(args, brokenOutput{}, io.Discard, testInfo,
				WithStdin(strings.NewReader("body")), WithBaseURL(m.URL()))
			if code != ExitOK {
				t.Fatalf("delivered message returned exit %d", code)
			}
			if got := len(m.Requests()); got != 1 {
				t.Fatalf("requests = %d, want 1", got)
			}
			store := &deferral.Store{Dir: dir}
			queued, err := store.List()
			if err != nil {
				t.Fatal(err)
			}
			if len(queued) != 0 {
				t.Fatalf("delivered message left queued: %v", queued)
			}
		})
	}
	if !strings.Contains(diagnostics.String(), "writing command output") {
		t.Fatalf("missing output failure diagnostic: %s", &diagnostics)
	}
}

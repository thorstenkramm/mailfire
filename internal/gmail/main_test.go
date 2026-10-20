package gmail

import (
	"os"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunWithPrivateTempDir(m)) }

package localapi

import (
	"independent-panel/internal/engine"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if code, handled := engine.Supervisor(); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

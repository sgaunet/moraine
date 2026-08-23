package cli_test

import (
	"os"
	"testing"

	"github.com/sgaunet/moraine/internal/configfile"
)

// TestMain takes the developer's own configuration file out of the picture for this
// whole package. Every test here drives cli.Execute, which now looks for
// ~/.config/moraine.yaml, so without this a real file on whichever machine runs
// `go test` would silently change what these tests observe — and CI and a laptop
// would disagree about it. An empty MORAINE_CONFIG means "no configuration file", so
// one setting covers the package instead of a fixture in every test.
//
// A test that *wants* a file calls t.Setenv, which overrides this for its duration.
func TestMain(m *testing.M) {
	// Plain os.Setenv, not t.Setenv (which needs a *testing.T), and done before any
	// test starts, so nothing races with it.
	if err := os.Setenv(configfile.EnvVar, ""); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

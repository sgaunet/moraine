package cli_test

import (
	"bytes"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
)

// commandSections are the sections that mirror a command one for one. "shared" is
// left out on purpose: it holds the three settings every command inherits, so it is a
// subset of sort's by design rather than a section that should cover it.
var commandSections = []string{"sort", "clean", "undo"}

// A flag added to sort, clean or undo must land on one of two lists: the settings a
// configuration file can hold, or the mode flags it deliberately cannot. Forgetting
// both would ship a flag nobody can put in a file, silently — which is the whole
// failure this test exists to make loud.
func TestEveryCommandFlagIsSettableOrDeliberatelyNot(t *testing.T) {
	for _, section := range commandSections {
		t.Run(section, func(t *testing.T) {
			settable := slices.Sorted(slices.Values(cli.SettingFlags(section)))
			excluded := cli.UnconfigurableFlags()
			for _, name := range cli.CommandFlags(section) {
				switch {
				case slices.Contains(settable, name), slices.Contains(excluded, name):
					continue
				case name == "help" || name == "config":
					continue // cobra's own, and the root's persistent file selector
				default:
					t.Errorf("--%s is neither writable by `moraine config set %s` "+
						"nor listed in unconfigurable; add it to one of them",
						name, section)
				}
			}
		})
	}
}

// The reverse: a setting the table claims must be a flag the command really has,
// otherwise `config set` would offer to write something no run reads. --output is the
// one exception — it is persistent on the root command rather than on any subcommand.
func TestEverySettableFlagExistsOnItsCommand(t *testing.T) {
	for _, section := range cli.Sections() {
		t.Run(section, func(t *testing.T) {
			registered := cli.CommandFlags(section)
			for _, name := range cli.SettingFlags(section) {
				if name == "output" || slices.Contains(registered, name) {
					continue
				}
				t.Errorf("`config set %s` offers --%s, which %s does not have", section, name, section)
			}
		})
	}
}

// `config set <section> --help` must actually offer every setting of that section:
// the table and the registration are separate steps, and a section that registered
// nothing would still pass the two tests above.
func TestConfigSetHelpOffersEverySetting(t *testing.T) {
	for _, section := range cli.Sections() {
		t.Run(section, func(t *testing.T) {
			var out bytes.Buffer
			if code := cli.Execute("dev", []string{"config", "set", section, "--help"}, &out, io.Discard); code != 0 {
				t.Fatalf("exit = %d, want 0", code)
			}
			for _, name := range cli.SettingFlags(section) {
				if !strings.Contains(out.String(), "--"+name) {
					t.Errorf("`config set %s --help` does not offer --%s:\n%s", section, name, out.String())
				}
			}
			for _, name := range cli.UnconfigurableFlags() {
				if strings.Contains(out.String(), "--"+name+" ") && name != "dry-run" {
					t.Errorf("`config set %s --help` offers --%s, which a file must not set", section, name)
				}
			}
		})
	}
}

// The shared section must stay a subset of what sort accepts, since a top-level key
// is inherited by every command and sort is the command with the most settings.
func TestSharedSettingsAreASubsetOfSort(t *testing.T) {
	sortFlags := cli.SettingFlags("sort")
	for _, name := range cli.SettingFlags("shared") {
		if !slices.Contains(sortFlags, name) {
			t.Errorf("the shared section offers --%s, which sort does not accept", name)
		}
	}
}

// The YAML keys are the flag names in snake_case. Stating it as a test keeps a new
// setting from arriving with a key spelled some other way, which would make the file,
// the flags and the JSON output disagree about what to call the same thing.
func TestYAMLKeysAreTheFlagNamesInSnakeCase(t *testing.T) {
	for _, section := range cli.Sections() {
		flags, keys := cli.SettingFlags(section), cli.SettingKeys(section)
		for i, flag := range flags {
			if want := strings.ReplaceAll(flag, "-", "_"); keys[i] != want {
				t.Errorf("%s: --%s is spelled %q in the file, want %q", section, flag, keys[i], want)
			}
		}
	}
}

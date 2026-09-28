package config

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// maxThemeDescription bounds one theme description, in characters. Every option of
// the decision service's choice question shares one option budget (192 to 256
// tokens on Laya), so a long description costs the other themes theirs.
const maxThemeDescription = 120

// parseThemeDescriptions turns --theme-description slug=text entries into a map. Each
// entry is split on its first "=", so the text may carry commas and further "=" signs.
// New calls it only when there are entries: no map at all is what leaves every prompt
// exactly as it was.
func parseThemeDescriptions(entries, themes []string, fallback string) (map[string]string, error) {
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		slug, text, ok := strings.Cut(e, "=")
		slug, text = strings.TrimSpace(slug), strings.TrimSpace(text)
		switch {
		case !ok:
			return nil, fmt.Errorf("--theme-description %q: expected slug=text", e)
		case !slices.Contains(themes, slug) && slug != fallback:
			return nil, fmt.Errorf("--theme-description %q: %q is neither a configured theme nor the fallback theme", e, slug)
		case text == "":
			return nil, fmt.Errorf("--theme-description %q: the description is empty", e)
		case strings.ContainsAny(text, "\r\n"):
			return nil, fmt.Errorf("--theme-description for %q: the description must be one line", slug)
		case utf8.RuneCountInString(text) > maxThemeDescription:
			return nil, fmt.Errorf("--theme-description for %q: the description is longer than %d characters",
				slug, maxThemeDescription)
		}
		if _, dup := out[slug]; dup {
			return nil, fmt.Errorf("--theme-description: %q is described twice", slug)
		}
		out[slug] = text
	}
	return out, nil
}

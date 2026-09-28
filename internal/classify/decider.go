package classify

import (
	"context"

	"github.com/sgaunet/moraine/internal/photo"
)

// Decider picks a theme from words rather than pixels: given what the vision model
// said each sampled photo shows, it chooses one of the offered themes. It is
// implemented by *decide.Client (a systemone decision service) and by plain fakes in
// tests, the same consumer-side seam PreviewExtractor is.
type Decider interface {
	// Ready proves the service can answer this run's question — reachable,
	// authorised, and accepting the model and the option list — before the first
	// real decision. It is called at most once per run.
	Ready(ctx context.Context, d Decision) error
	// Decide returns a Decided verdict for the descriptions. Choosing d.Fallback is
	// an abstention: a Decided verdict with an empty Theme.
	Decide(ctx context.Context, d Decision) (Verdict, error)
}

// Decision is what a Decider is asked about one event, or about one photo of it on
// the --vote path. It carries text only: images never leave for the decision
// service.
type Decision struct {
	// Descriptions are the vision model's accounts of the sampled photos, in photo
	// order.
	Descriptions []string
	// Context is the capture context the single-call prompt already sends (see
	// ContextLines).
	Context []string
	// Options are the candidate themes, configured themes first in configuration
	// order, then the fallback.
	Options []ThemeOption
	// Fallback is the option whose selection means "none of the themes fit".
	Fallback string
}

// ThemeOption is one candidate theme and what it covers. Description is empty for
// a custom theme nobody described.
type ThemeOption struct {
	Slug        string
	Description string
}

// EffectiveDescriptions lists the candidate themes with what each covers: the user's
// description where there is one, else the built-in one, else none. The fallback
// comes last, with a built-in description of its own, since choosing it abstains.
func EffectiveDescriptions(themes []string, fallback string, overrides map[string]string) []ThemeOption {
	out := make([]ThemeOption, 0, len(themes)+1)
	for _, t := range themes {
		out = append(out, ThemeOption{Slug: t, Description: describeTheme(t, themeHints[t], overrides)})
	}
	return append(out, ThemeOption{Slug: fallback, Description: describeTheme(fallback, fallbackDescription, overrides)})
}

// describeTheme returns the user's description of slug, or builtIn.
func describeTheme(slug, builtIn string, overrides map[string]string) string {
	if d, ok := overrides[slug]; ok {
		return d
	}
	return builtIn
}

// ContextLines returns the capture-context facts the cluster carries — when, how
// high, where — one per line, as the single-call prompt renders them.
func ContextLines(c photo.Cluster) []string {
	return metadataLines(c)
}

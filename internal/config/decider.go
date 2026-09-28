package config

import (
	"fmt"
	"net/url"
	"strings"
)

// Decider selects the decision service that picks a theme from photo descriptions,
// or none — the vision model then picks the theme itself, as it always has.
type Decider string

// The supported decision services.
const (
	// DeciderOff keeps the single-call vision classification.
	DeciderOff Decider = "off"
	// DeciderLaya is a self-hosted Laya server (laya-serve).
	DeciderLaya Decider = "laya"
	// DeciderJev is the hosted TypeSafe Jev service.
	DeciderJev Decider = "jev"
)

// Backend defaults, applied when --decider-url or --decider-model is left empty.
const (
	DefaultDecider   = string(DeciderOff)
	DefaultLayaURL   = "http://127.0.0.1:8000"
	DefaultJevURL    = "https://api.typesafe.ai"
	DefaultLayaModel = "multilingual"
	DefaultJevModel  = "jev-latest"
)

// The environment variables the transport reads a decision service's API key from.
// Named here so the usage error below and the transport cannot disagree.
const (
	EnvLayaAPIKey = "LAYA_API_KEY"
	EnvJevAPIKey  = "TYPESAFE_API_KEY"
)

// Option-count limits for one choice question, counting the fallback theme, which
// is offered too. Laya's server refuses more than 100 with a 413 before inference;
// Jev documents 255.
const (
	maxLayaOptions = 100
	maxJevOptions  = 255
)

// deciderSettings is the validated form of the decider options.
type deciderSettings struct {
	decider       Decider
	url           string
	model         string
	minConfidence float64
	apiKey        string
}

// ParseDecider maps a textual --decider value to a Decider. Empty is off, so a
// configuration file that omits the key behaves like one that never mentioned it.
func ParseDecider(s string) (Decider, error) {
	switch d := Decider(strings.ToLower(strings.TrimSpace(s))); d {
	case "", DeciderOff:
		return DeciderOff, nil
	case DeciderLaya, DeciderJev:
		return d, nil
	default:
		return "", fmt.Errorf("--decider invalid %q: expected off|laya|jev", s)
	}
}

// APIKeyEnv names the environment variable a decider's key is read from, or "" for
// off.
func (d Decider) APIKeyEnv() string {
	switch d {
	case DeciderLaya:
		return EnvLayaAPIKey
	case DeciderJev:
		return EnvJevAPIKey
	case DeciderOff:
		return ""
	default:
		return ""
	}
}

// resolveDecider validates the decider options against the parsed theme list. With
// the decider off, the other settings are not checked: a configuration file may
// keep its URL while a flag turns the feature off.
func resolveDecider(o Options, themes int) (deciderSettings, error) {
	if o.DeciderMinConfidence < 0 || o.DeciderMinConfidence > 1 {
		return deciderSettings{}, fmt.Errorf("--decider-min-confidence must be between 0 and 1 (got %g)",
			o.DeciderMinConfidence)
	}
	d, err := ParseDecider(o.Decider)
	if err != nil {
		return deciderSettings{}, err
	}
	s := deciderSettings{decider: d, minConfidence: o.DeciderMinConfidence}
	if d == DeciderOff {
		return s, nil
	}

	s.url, s.model, s.apiKey = strings.TrimSpace(o.DeciderURL), strings.TrimSpace(o.DeciderModel), o.DeciderAPIKey
	defURL, defModel, maxOptions := DefaultLayaURL, DefaultLayaModel, maxLayaOptions
	if d == DeciderJev {
		defURL, defModel, maxOptions = DefaultJevURL, DefaultJevModel, maxJevOptions
		if s.apiKey == "" {
			return deciderSettings{}, fmt.Errorf("--decider jev needs an API key: export %s", EnvJevAPIKey)
		}
	}
	if s.url == "" {
		s.url = defURL
	} else if err := checkServiceURL(s.url); err != nil {
		return deciderSettings{}, err
	}
	if s.model == "" {
		s.model = defModel
	}
	if themes+1 > maxOptions {
		return deciderSettings{}, fmt.Errorf("--themes: %d themes plus the fallback exceed the %d options --decider %s accepts",
			themes, maxOptions, d)
	}
	return s, nil
}

// checkServiceURL accepts an absolute http or https URL naming a host.
func checkServiceURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("--decider-url invalid %q: expected an absolute http(s) URL", raw)
	}
	return nil
}

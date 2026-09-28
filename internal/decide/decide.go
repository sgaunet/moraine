// Package decide asks a systemone decision service — a self-hosted Laya server or
// the hosted TypeSafe Jev — which configured theme a set of photo descriptions
// belongs to. It is the second half of describe-then-decide: the vision model turns
// pixels into words (internal/classify), and this package turns the words into a
// theme, with a confidence on the service's own scale.
//
// It owns the wire protocol (through github.com/sgaunet/gutcheck), the request
// moraine sends (request.go) and the mapping of the answer back to a verdict
// (answer.go). internal/classify sees only its classify.Decider interface, which
// *Client implements, so the classify tests need no HTTP fake for a concern that is
// not image classification.
//
// Every call is bounded: a decision attempt by decisionTimeout and the readiness
// check by readyTimeout, both under the run's context. A failure is retried with
// backoff on a connection error, 408, 429 or 5xx (honouring Retry-After), but never
// after a timeout: a CPU inference that timed out is still running server-side, and
// asking again only queues a second copy of the same work behind it.
package decide

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/sgaunet/gutcheck"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/config"
)

// Backend names the kind of decision service.
type Backend string

// The supported backends. They speak the same protocol but report confidence
// differently (see confidenceOf), and only Jev requires a key.
const (
	Laya Backend = "laya"
	Jev  Backend = "jev"
)

// Config is what a Client needs. URL and Model are sent as given: the defaults are
// resolved by internal/config, so nothing here falls back to the environment.
type Config struct {
	Backend Backend
	URL     string
	Model   string
	APIKey  string
}

const (
	// decisionTimeout bounds one decision attempt, the same budget one vision-model
	// call gets. gutcheck's own 10s default would time out a healthy CPU server
	// rebuilding a cold checkpoint, which Laya documents at 7-10s.
	decisionTimeout = 60 * time.Second
	// readyTimeout bounds the readiness check, which may be the request that loads the
	// checkpoint on a lazily-started server — the same allowance the vision model's
	// warm-up gets.
	readyTimeout = 2 * time.Minute
	// idleConnsPerHost sizes the keep-alive pool for the --vote fan-out, as the Ollama
	// client does; http.DefaultClient's 2 would reopen a connection per decision.
	idleConnsPerHost = 8
)

// timeouts groups the bounds a Client applies, so a test can shorten them.
type timeouts struct {
	decision time.Duration
	ready    time.Duration
	backoff  time.Duration
}

// Client asks one decision service. It is safe for concurrent use.
type Client struct {
	// Logger receives one debug record per decision. Nil means slog.Default().
	Logger *slog.Logger

	backend Backend
	model   string
	apiKey  string
	http    *http.Client
	// decider serves decisions and ready the readiness check. They differ only in the
	// per-attempt timeout, which gutcheck fixes when a client is built.
	decider *gutcheck.Client
	ready   *gutcheck.Client
	limits  timeouts
}

// New builds a Client for cfg.
func New(cfg Config) (*Client, error) {
	return newClient(cfg, timeouts{decision: decisionTimeout, ready: readyTimeout})
}

func newClient(cfg Config, limits timeouts) (*Client, error) {
	c := &Client{backend: cfg.Backend, model: cfg.Model, apiKey: cfg.APIKey, http: newHTTPClient(), limits: limits}
	var err error
	if c.decider, err = c.gutcheckClient(cfg, limits.decision); err != nil {
		return nil, err
	}
	if c.ready, err = c.gutcheckClient(cfg, limits.ready); err != nil {
		return nil, err
	}
	return c, nil
}

// newHTTPClient clones http.DefaultTransport, keeping its proxy, dial and TLS
// settings, and widens only the keep-alive pool.
func newHTTPClient() *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: idleConnsPerHost}}
	}
	tr := base.Clone()
	tr.MaxIdleConnsPerHost = idleConnsPerHost
	return &http.Client{Transport: tr}
}

// Ready asks the run's real question — every offered theme — about a fixed one-line
// state, and discards the answer. A health endpoint could not stand in for it: Laya
// answers /health without authentication, so only a real request proves the key,
// the model name and the option budget together. On a lazily-loaded server it also
// builds the checkpoint, so the first real decision does not pay for that.
func (c *Client) Ready(ctx context.Context, d classify.Decision) error {
	ctx, cancel := context.WithTimeout(ctx, c.limits.ready)
	defer cancel()
	if _, err := c.ready.Evaluate(ctx, request(readinessState, d.Options, c.model)); err != nil {
		return c.explain(err)
	}
	return nil
}

// Decide asks which theme the descriptions belong to.
func (c *Client) Decide(ctx context.Context, d classify.Decision) (classify.Verdict, error) {
	resp, err := c.decider.Evaluate(ctx, buildRequest(d, c.model))
	if err != nil {
		return classify.Verdict{}, c.explain(err)
	}
	offered := make([]string, 0, len(d.Options))
	for _, o := range d.Options {
		offered = append(offered, o.Slug)
	}
	v, err := verdictOf(resp, c.backend, offered, d.Fallback)
	if err != nil {
		return classify.Verdict{}, err
	}
	c.log().Debug("decision", "theme", v.Theme, "confidence", v.Confidence,
		"probabilities", resp.Answers[questionID].Probabilities, "backend", string(c.backend), "model", resp.Model)
	return v, nil
}

// gutcheckClient builds a gutcheck client whose every setting is explicit. The key is
// always passed, even empty, so gutcheck's own environment lookup cannot become a
// second, hidden source of credentials.
func (c *Client) gutcheckClient(cfg Config, attempt time.Duration) (*gutcheck.Client, error) {
	retry := gutcheck.DefaultRetryPolicy()
	retry.RetryTimeouts = false
	if c.limits.backoff > 0 {
		retry.BackoffInitial, retry.BackoffMax = c.limits.backoff, c.limits.backoff
	}
	opts := []gutcheck.Option{
		gutcheck.WithBaseURL(cfg.URL),
		gutcheck.WithAPIKey(cfg.APIKey),
		gutcheck.WithDefaultModel(cfg.Model),
		gutcheck.WithHTTPClient(c.http),
		gutcheck.WithTimeout(attempt),
		gutcheck.WithRetryPolicy(retry),
	}
	var (
		gc  *gutcheck.Client
		err error
	)
	if cfg.Backend == Jev {
		gc, err = gutcheck.New(opts...)
	} else {
		gc, err = gutcheck.NewLaya(cfg.URL, opts...)
	}
	if err != nil {
		return nil, fmt.Errorf("building the %s client: %w", cfg.Backend, err)
	}
	return gc, nil
}

// explain adds what a user can act on to an authentication failure: which variable
// the key is read from. It names the variable, never its value. Every other error is
// returned as gutcheck reported it, which already names the endpoint and the status.
func (c *Client) explain(err error) error {
	if !errors.Is(err, gutcheck.ErrAuthentication) && !errors.Is(err, gutcheck.ErrPermissionDenied) {
		return fmt.Errorf("asking the decision service: %w", err)
	}
	env := config.Decider(c.backend).APIKeyEnv()
	if c.apiKey == "" {
		return fmt.Errorf("the decision service wants an API key: export %s: %w", env, err)
	}
	return fmt.Errorf("the decision service refused the API key in %s: %w", env, err)
}

func (c *Client) log() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

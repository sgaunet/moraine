package classify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
)

// PreviewExtractor turns a file whose pixels Go cannot decode — RAW or HEIC —
// into model-viewable JPEG bytes. It is implemented by *rawpreview.Extractor
// (which copies out a RAW's embedded preview) and by *heicpreview.Converter
// (which decodes a HEIC with an external program). A nil extractor disables that
// input entirely: such photos are then skipped for the model.
type PreviewExtractor interface {
	Extract(ctx context.Context, path string) ([]byte, error)
}

// OllamaClassifier asks a local Ollama vision model to pick one theme from the
// configured set for a cluster. Every call is bounded by a context timeout and
// retried once on a transient error. Any failure is the caller's cue to fall back.
type OllamaClassifier struct {
	BaseURL string
	Model   string
	Sample  int
	Themes  []string
	Timeout time.Duration
	// HTTP is the client every call goes through. NewOllama sizes its transport's
	// connection pool for concurrent use; replacing it with a bare &http.Client{}
	// reinstates net/http's two-connections-per-host default.
	HTTP   *http.Client
	Logger *slog.Logger
	// KeepAlive is how long Ollama should keep the model resident after a call
	// (an Ollama duration string, e.g. "10m"). Empty leaves Ollama's own default.
	KeepAlive string
	// RawPreview extracts a camera RAW's embedded JPEG (exiftool). Optional: a nil
	// value skips RAW photos for the model.
	RawPreview PreviewExtractor
	// HEICPreview decodes a HEIC into JPEG (an external converter). Optional and
	// separate from RawPreview because the two formats need different programs:
	// a HEIC written by an iPhone embeds no JPEG for exiftool to copy out.
	HEICPreview PreviewExtractor
	// Vote classifies a group larger than SmallGroupMax one photo at a time and
	// lets the sampled photos vote (see vote.go). It costs one model call per
	// sampled photo instead of one per group, so it is opt-in.
	Vote bool

	// VoteWorkers bounds how many of a group's votes are in flight at once. Zero
	// means defaultVoteWorkers. It is a field rather than a flag so a test can pin
	// the serial path and prove the verdict does not depend on the order the votes
	// came back in.
	VoteWorkers int

	// warmOnce loads the model before the first classification and never again.
	warmOnce sync.Once
}

// NewOllama builds an OllamaClassifier with sane defaults for the given themes.
func NewOllama(baseURL, model string, sample int, themes []string) *OllamaClassifier {
	return &OllamaClassifier{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Model:     model,
		Sample:    sample,
		Themes:    themes,
		Timeout:   60 * time.Second,
		HTTP:      newHTTPClient(),
		Logger:    slog.Default(),
		KeepAlive: DefaultKeepAlive,
	}
}

// Status is the outcome of an Ollama Preflight check.
type Status int

const (
	// StatusReady means Ollama answered and the configured model is installed.
	StatusReady Status = iota
	// StatusUnreachable means the Ollama endpoint could not be contacted.
	StatusUnreachable
	// StatusModelMissing means Ollama is reachable but the model is not pulled.
	StatusModelMissing
)

type tagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// newHTTPClient returns the client every Ollama call goes through. The transport is
// cloned from http.DefaultTransport rather than built fresh, so ProxyFromEnvironment,
// the dial and TLS-handshake timeouts and ForceAttemptHTTP2 all survive: a user
// behind an HTTP proxy would otherwise lose it to this one-line optimisation. Only
// MaxIdleConnsPerHost is changed.
//
// No ResponseHeaderTimeout is set, deliberately: a cold vision model can take minutes
// to produce its first byte, and the bound that belongs here is the per-call context
// deadline (see bounded), not a transport-wide one that would abort a legitimate load.
func newHTTPClient() *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: idleConnsPerHost}}
	}
	tr := base.Clone()
	tr.MaxIdleConnsPerHost = idleConnsPerHost
	return &http.Client{Transport: tr}
}

// drainAndClose returns a connection to the keep-alive pool and then closes it.
//
// net/http only pools a connection once its body has reached EOF. Both callers read
// through an io.LimitReader, so a reply larger than maxResponseBytes is left
// part-read, and closing it there costs a fresh TCP handshake on the next call. The
// drain is bounded twice over, because an unbounded read of a hostile or runaway
// stream is its own denial of service: by drainLimit bytes, and by the request
// context — reads on the body honour its deadline, so the same Timeout that bounds
// the call bounds the drain.
func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, drainLimit))
	_ = body.Close()
}

// Preflight checks that Ollama is reachable and the configured model is
// installed, by querying GET {BaseURL}/api/tags. It is bounded by a short
// timeout and never blocks the run: any problem is reported as a Status the
// caller logs and acts on.
func (o *OllamaClassifier) Preflight(ctx context.Context) Status {
	timeout := o.Timeout
	if timeout <= 0 || timeout > 10*time.Second {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/api/tags", nil)
	if err != nil {
		return StatusUnreachable
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return StatusUnreachable
	}
	defer func() { drainAndClose(resp.Body) }()
	if resp.StatusCode != http.StatusOK {
		return StatusUnreachable
	}

	var tags tagsResponse
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return StatusUnreachable
	}
	if err := json.Unmarshal(body, &tags); err != nil {
		return StatusUnreachable
	}
	configHasTag := strings.Contains(o.Model, ":")
	for _, m := range tags.Models {
		if m.Name == o.Model {
			return StatusReady
		}
		// If the configured model omits a tag, match any installed tag of it.
		if !configHasTag && baseModel(m.Name) == o.Model {
			return StatusReady
		}
	}
	return StatusModelMissing
}

// baseModel strips an Ollama tag suffix (e.g. "qwen3-vl:8b" → "qwen3-vl").
func baseModel(name string) string {
	if i := strings.IndexByte(name, ':'); i >= 0 {
		return name[:i]
	}
	return name
}

type chatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Format   any           `json:"format,omitempty"`
	Options  chatOptions   `json:"options"`
	// KeepAlive asks Ollama to hold the model in memory for this long after the
	// call, so the clusters that follow do not each pay the load again.
	KeepAlive string `json:"keep_alive,omitempty"`
}

// chatOptions pins Ollama's decoding so the same cluster classifies to the same
// theme on every run: temperature 0 removes sampling and a fixed seed removes
// the remaining randomness. Determinism also gives the retry-once logic meaning
// (a retry after a transient transport error yields the same answer).
type chatOptions struct {
	Temperature float64 `json:"temperature"`
	Seed        int     `json:"seed"`
}

// ollamaSeed is the fixed RNG seed sent with every request for reproducibility.
const ollamaSeed = 42

// defaultVoteWorkers bounds how many per-photo votes are in flight for one group.
//
// Two, and the reason is a timeout rather than politeness. Each vote's Timeout
// starts when its goroutine does, not when the server reaches it, and Ollama
// serialises requests per model unless OLLAMA_NUM_PARALLEL is raised — so the k-th
// concurrent vote spends (k-1) inference times of its own budget merely queued.
// With --sample 5 against a model taking 20s an image, a fan-out of 4 would leave
// the fourth vote waiting the full 60s and time out: the same wall-clock for one
// vote fewer. Two caps that exposure at a single queued request with a threefold
// margin, and still overlaps this tool's own work — preview extraction, downscaling,
// base64, JSON marshalling — with the server's inference.
const defaultVoteWorkers = 2

// DefaultKeepAlive is how long NewOllama asks Ollama to keep the model resident
// after a call. A run classifies one cluster after another, so unloading between
// them would re-pay the multi-second load of a vision model every time.
const DefaultKeepAlive = "10m"

// warmupTimeout bounds Warmup. It is deliberately independent of (and longer
// than) the classification Timeout: loading an 8B vision model is slow, and the
// whole point of warming up is to keep that cost out of a classification budget.
const warmupTimeout = 2 * time.Minute

// maxResponseBytes caps what is read from an Ollama response, so a runaway or
// hostile endpoint cannot make the process allocate without bound.
const maxResponseBytes = 4 << 20

// drainLimit is how much of an over-long response is read past maxResponseBytes so
// that its connection can go back in the keep-alive pool. Equal to maxResponseBytes
// on purpose: having already agreed to read 4 MiB, reading at most 4 MiB more to
// save a connection is the same order of magnitude, and past that the body is not
// worth rescuing — closing early costs one connection, which is where we started.
const drainLimit = maxResponseBytes

// idleConnsPerHost sizes the keep-alive pool. net/http's default is 2
// (http.DefaultMaxIdleConnsPerHost), which is invisible while calls are serial and
// becomes a fresh TCP handshake per call the moment they are not. It caps the pool,
// not the request rate, so over-sizing costs a couple of idle sockets to a local
// server while under-sizing silently reintroduces the handshake this exists to
// remove.
const idleConnsPerHost = 8

type chatResponse struct {
	Message chatMessage `json:"message"`
}

// schemaProperty is one property of a structured-output JSON Schema. No numeric
// bounds are sent: Ollama turns the schema into a decoding grammar, and support for
// minimum/maximum there is uneven, so the range is stated in the prompt and enforced
// in Go (see reportedConfidence).
type schemaProperty struct {
	Type string   `json:"type"`
	Enum []string `json:"enum,omitempty"`
}

// responseSchema is the JSON Schema sent in chatRequest.Format. Its enum
// constrains Ollama's decoding so the model cannot emit an out-of-set theme.
type responseSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]schemaProperty `json:"properties"`
	Required   []string                  `json:"required"`
}

// structuredAnswer is the shape the model is asked to return. Confidence is a
// pointer so an answer that omits it is distinguishable from one that reports zero;
// both end up as "not reported", but only the pointer says which happened.
type structuredAnswer struct {
	Category   string   `json:"category"`
	Confidence *float64 `json:"confidence"`
}

// slugNonWord matches runs of characters that are not slug-safe.
var slugNonWord = regexp.MustCompile(`[^a-z0-9]+`)

// abstainCategory is the sentinel the model may return when no theme fits. It is
// added to the schema enum and offered in the prompt; a "none" answer makes
// Classify return ("", nil) so Label uses the configured fallback theme instead
// of forcing an arbitrary theme onto e.g. a receipt or screenshot.
const abstainCategory = "none"

// themeHints maps each built-in default theme to a short description so the
// vision model matches the scene instead of guessing at a bare slug. Custom
// themes (not in this map) are listed by slug alone.
var themeHints = map[string]string{
	"mountain":       "mountains, peaks, alpine landscapes, hiking, snow, skiing",
	"special-events": "weddings, parties, concerts, ceremonies, celebrations",
	"cook":           "food, meals, cooking, plated dishes, restaurants",
	"family":         "people, portraits, family gatherings, children, daily life",
}

// Classify returns the model's verdict for the cluster, or an error on failure
// (transport, timeout, or an answer outside the configured set). An abstention — the
// model saying nothing fits — is a zero-Theme Verdict with a nil error.
//
// With Vote set, a group larger than SmallGroupMax is classified one photo at a time
// and the sampled photos vote; see classifyByVote.
func (o *OllamaClassifier) Classify(ctx context.Context, c photo.Cluster) (Verdict, error) {
	// callCtx bounds the sampling and, without voting, the one model call; ctx stays
	// the run's own deadline, so the one-off model load below is not charged to a
	// single cluster's budget.
	callCtx, cancel := o.bounded(ctx)
	defer cancel()

	images := o.sampleImages(callCtx, c)
	if len(images) == 0 {
		o.log().Warn("classification skipped: no usable image (a RAW or HEIC with no extractable preview is not sent to the model)",
			"group_size", len(c.Photos))
		return Verdict{}, errors.New("no usable image to classify")
	}

	// Load the model before the timed requests rather than inside them. Done here,
	// after the sampling, so a run that never finds a usable image — or never
	// classifies at all, as an --incremental pass over an unchanged library — does
	// not pull gigabytes into memory for nothing.
	o.ensureLoaded(ctx)

	if o.Vote && len(c.Photos) > SmallGroupMax {
		// Each vote is a model call of its own, so it gets its own Timeout budget
		// rather than a share of this one: one slow photo must not spend the next
		// photo's time. Hence ctx, not callCtx.
		return o.classifyByVote(ctx, c, images)
	}
	return o.ask(callCtx, c, images)
}

// chatAttempts and chatBackoff bound the retry of a transient failure. The delay
// doubles per attempt (200ms, then 400ms) and is charged to the same o.Timeout
// budget as the calls themselves, so retrying can never extend a run's deadline.
const (
	chatAttempts = 3
	chatBackoff  = 200 * time.Millisecond
)

// sleepCtx waits for d and reports whether it elapsed. It returns false as soon
// as ctx is done, so a backoff never outlives an interrupt or the call timeout.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Warmup asks Ollama to load the model into memory now. Ollama loads (and, with
// keep_alive, holds) a model when /api/chat is called with an empty message list.
// Doing it here moves the multi-second cold load of a vision model out of the
// first classification's timeout budget, which is where it would otherwise land.
// Any failure is the caller's cue to log and carry on: a cold model still
// classifies, just slower.
func (o *OllamaClassifier) Warmup(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, warmupTimeout)
	defer cancel()

	payload, err := json.Marshal(chatRequest{
		Model:     o.Model,
		Messages:  []chatMessage{}, // empty: load the model, answer nothing
		Options:   chatOptions{Temperature: 0, Seed: ollamaSeed},
		KeepAlive: o.KeepAlive,
	})
	if err != nil {
		return fmt.Errorf("encoding ollama warm-up request: %w", err)
	}
	if _, err := o.postChat(ctx, payload); err != nil {
		return fmt.Errorf("warming up model %q: %w", o.Model, err)
	}
	return nil
}

// bounded derives a per-call context from ctx, applying Timeout when one is set.
func (o *OllamaClassifier) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	if o.Timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, o.Timeout)
}

// ask sends one classification request for the given images and returns the model's
// verdict, retrying only a transient failure. A deterministic failure (a rejected
// category, a malformed request) is returned at once: decoding is pinned, so asking
// the same question again cannot change the answer.
func (o *OllamaClassifier) ask(ctx context.Context, c photo.Cluster, images []string) (Verdict, error) {
	payload, err := o.payload(c, images)
	if err != nil {
		return Verdict{}, err
	}
	o.log().Debug("contacting model", "url", o.BaseURL, "model", o.Model, "images", len(images))

	var lastErr error
	attempt := 0
	for attempt < chatAttempts {
		attempt++
		v, err := o.doChat(ctx, payload)
		if err == nil {
			return v, nil
		}
		lastErr = err
		if !errors.Is(err, errTransient) {
			break // deterministic answer (rejected category, bad request): asking again cannot help
		}
		if attempt == chatAttempts {
			break
		}
		if !sleepCtx(ctx, chatBackoff<<(attempt-1)) {
			break // timeout/cancel: do not keep retrying
		}
	}
	o.log().Warn("model unavailable or answer rejected — fallback",
		"url", o.BaseURL, "model", o.Model, "attempts", attempt, "err", lastErr)
	return Verdict{}, fmt.Errorf("ollama unavailable after %d attempt(s): %w", attempt, lastErr)
}

// payload encodes one chat request carrying the given images.
func (o *OllamaClassifier) payload(c photo.Cluster, images []string) ([]byte, error) {
	payload, err := json.Marshal(chatRequest{
		Model:     o.Model,
		Stream:    false,
		Format:    o.schema(),
		Options:   chatOptions{Temperature: 0, Seed: ollamaSeed},
		KeepAlive: o.KeepAlive,
		Messages: []chatMessage{
			{Role: "system", Content: o.systemPrompt()},
			{Role: "user", Content: o.userPrompt(c), Images: images},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encoding ollama request: %w", err)
	}
	return payload, nil
}

// log returns the configured logger or the default, never nil.
func (o *OllamaClassifier) log() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}

// systemPrompt is the stable output contract sent as the system message. It
// fixes the model's role and the JSON shape; the per-request category list lives
// in userPrompt. Naming JSON here is recommended alongside the Format schema.
func (o *OllamaClassifier) systemPrompt() string {
	return "You are an image classifier. You are shown several photos from the same event. " +
		`Respond ONLY with a JSON object of the form ` +
		`{"category": "<one allowed category>", "confidence": <number between 0 and 1>}. ` +
		"The category MUST be exactly one value from the allowed list (or \"none\"), in lowercase, " +
		"with no extra text. The confidence is how sure you are of that category: 1 means certain, " +
		"0.5 means it is a guess between two categories."
}

// userPrompt carries the per-request data: the allowed categories (each with a
// short description), what the cluster's EXIF already knows about the photos, and
// the task, including the option to abstain with "none".
func (o *OllamaClassifier) userPrompt(c photo.Cluster) string {
	var b strings.Builder
	b.WriteString("Allowed categories:\n")
	for _, t := range o.Themes {
		if hint := themeHints[t]; hint != "" {
			fmt.Fprintf(&b, "- %s: %s\n", t, hint)
		} else {
			fmt.Fprintf(&b, "- %s\n", t)
		}
	}
	fmt.Fprintf(&b, "- %s: the photos do not clearly fit any category above\n", abstainCategory)
	b.WriteString(metadataBlock(c))
	b.WriteString("Pick the single category that best describes these photos, ")
	b.WriteString("and say how confident you are of it. ")
	fmt.Fprintf(&b, "If none of them clearly fits, answer %q.", abstainCategory)
	return b.String()
}

// metadataBlock renders what the cluster's EXIF already knows — when, how high,
// and where — as a few lines of text, or "" when it knows nothing. It is a cheap
// multimodal boost: the model otherwise sees pixels only, and altitude in
// particular separates an alpine scene from a garden one no crop can settle.
//
// The wording keeps the metadata subordinate on purpose. A meal photographed in a
// refuge at 2400 m is still "cook", and a model told to weigh altitude above what
// it can see would get that wrong.
func metadataBlock(c photo.Cluster) string {
	lines := metadataLines(c)
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nWhat the photo metadata says (context only — the images decide):\n")
	for _, l := range lines {
		fmt.Fprintf(&b, "- %s\n", l)
	}
	b.WriteString("\n")
	return b.String()
}

// metadataLines returns one line per fact the cluster actually carries, in a
// fixed order. A fact that is missing produces no line rather than an "unknown",
// which would only spend tokens telling the model nothing.
func metadataLines(c photo.Cluster) []string {
	var lines []string
	if l := takenLine(c); l != "" {
		lines = append(lines, l)
	}
	if alt, ok := maxAltitude(c); ok {
		lines = append(lines, fmt.Sprintf("highest altitude: %.0f m above sea level", alt))
	}
	if gps, ok := firstGPS(c); ok {
		lines = append(lines, fmt.Sprintf("location: %.2f, %.2f", gps.Lat, gps.Lng))
	}
	return lines
}

// takenLine describes when the photos were taken, as an instant or a span. It is
// empty for a cluster with no usable capture time (nothing dates a hand-built one).
func takenLine(c photo.Cluster) string {
	if c.Start.IsZero() || c.End.IsZero() {
		return ""
	}
	const layout = "2006-01-02 15:04"
	// Compare the rendered values, not the instants: a burst spanning a few seconds
	// formats to one minute, and "11:13 to 11:13" would be noise.
	start, end := c.Start.Format(layout), c.End.Format(layout)
	if start == end {
		return "taken: " + start + " (local time)"
	}
	return "taken: " + start + " to " + end + " (local time)"
}

// maxAltitude returns the highest altitude recorded across the cluster.
func maxAltitude(c photo.Cluster) (float64, bool) {
	var best float64
	found := false
	for _, p := range c.Photos {
		if p.Altitude == nil {
			continue
		}
		if !found || *p.Altitude > best {
			best, found = *p.Altitude, true
		}
	}
	return best, found
}

// firstGPS returns the first coordinate recorded in the cluster. One is enough:
// the photos of an event are, by construction, close together in time and place.
func firstGPS(c photo.Cluster) (photo.LatLng, bool) {
	for _, p := range c.Photos {
		if p.GPS != nil {
			return *p.GPS, true
		}
	}
	return photo.LatLng{}, false
}

// schema constrains the model to answer with exactly one configured theme, or
// the abstain sentinel when nothing fits.
func (o *OllamaClassifier) schema() responseSchema {
	enum := make([]string, 0, len(o.Themes)+1)
	enum = append(enum, o.Themes...)
	enum = append(enum, abstainCategory)
	return responseSchema{
		Type: "object",
		Properties: map[string]schemaProperty{
			"category":   {Type: "string", Enum: enum},
			"confidence": {Type: "number"},
		},
		Required: []string{"category", "confidence"},
	}
}

// errTransient marks a failure that a later identical request might survive — a
// dropped connection, a busy or restarting server. Decoding is pinned (temperature
// 0, fixed seed), so a failure *not* wrapped in it is deterministic and retrying
// it would only re-ask a question already answered.
var errTransient = errors.New("transient failure")

// transient wraps err so the retry loop recognises it as worth another attempt.
func transient(err error) error {
	return fmt.Errorf("%w: %w", errTransient, err)
}

// retryableStatus reports whether an HTTP status says "try again later" rather
// than "this request is wrong". Ollama returns 503 while a model loads and 500 on
// an internal hiccup; a 4xx (bad model name, malformed body) never will.
func retryableStatus(code int) bool {
	return code >= http.StatusInternalServerError ||
		code == http.StatusRequestTimeout ||
		code == http.StatusTooManyRequests
}

// postChat sends payload to /api/chat and returns the raw response body, tagging
// the failures that deserve a retry. It is shared by Classify and Warmup.
func (o *OllamaClassifier) postChat(ctx context.Context, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.HTTP.Do(req)
	if err != nil {
		return nil, transient(err)
	}
	defer func() { drainAndClose(resp.Body) }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, transient(err) // a truncated read is a broken connection, not a verdict
	}
	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		if retryableStatus(resp.StatusCode) {
			return nil, transient(statusErr)
		}
		return nil, statusErr
	}
	return body, nil
}

// ensureLoaded warms the model up once per classifier, reporting the outcome. A
// failure is worth no more than a warning: the classification that follows will
// simply pay the load itself, or fail and fall back like any other model problem.
func (o *OllamaClassifier) ensureLoaded(ctx context.Context) {
	o.warmOnce.Do(func() {
		start := time.Now()
		if err := o.Warmup(ctx); err != nil {
			o.log().Warn("model warm-up failed: this classification may be slow", "err", err)
			return
		}
		o.log().Info("model loaded", "model", o.Model, "took", time.Since(start).Round(time.Millisecond))
	})
}

func (o *OllamaClassifier) doChat(ctx context.Context, payload []byte) (Verdict, error) {
	body, err := o.postChat(ctx, payload)
	if err != nil {
		return Verdict{}, err
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Verdict{}, fmt.Errorf("unreadable ollama response: %w", err)
	}
	// Prefer the structured {"category": …, "confidence": …} answer; fall back to
	// the raw content for models that ignore the Format schema (which then report no
	// confidence at all). slugifyAnswer reduces the category to a slug we validate
	// against the set (or the abstain sentinel).
	raw := strings.TrimSpace(parsed.Message.Content)
	answer, confidence := raw, 0.0
	var structured structuredAnswer
	if err := json.Unmarshal([]byte(raw), &structured); err == nil && structured.Category != "" {
		answer = structured.Category
		confidence = reportedConfidence(structured.Confidence)
	}
	slug := slugifyAnswer(answer)
	o.log().Debug("model answer", "raw", raw, "slug", slug, "confidence", confidence)
	if slug == abstainCategory {
		// Intentional abstention, not a failure: return a zero Verdict with a nil
		// error so the retry loop stops and Label uses the configured fallback.
		return Verdict{}, nil
	}
	if !inSet(slug, o.Themes) {
		return Verdict{}, fmt.Errorf("category out of set: %q", raw)
	}
	return Verdict{Theme: slug, Confidence: confidence}, nil
}

// reportedConfidence normalises what the model said about its own certainty. A
// missing value — and anything outside 0..1, such as a model that answers in
// percent — counts as "not reported" rather than as a number to threshold on: the
// schema carries no bounds Ollama can enforce, so this is the only guarantee there
// is, and a silently rescaled guess would be worse than none.
func reportedConfidence(c *float64) float64 {
	if c == nil || *c <= 0 || *c > 1 {
		return 0
	}
	return *c
}

// sampleImages selects the photos to send and returns their base64 content.
// Eligible photos are JPEG/PNG (read directly) or RAW/HEIC (preview via the
// extractor); unknown formats are excluded. A photo whose bytes cannot be obtained
// (read error, or no usable preview) is skipped, never fatal (FR-007).
func (o *OllamaClassifier) sampleImages(ctx context.Context, c photo.Cluster) []string {
	chosen := o.choosePhotos(c)
	if len(chosen) == 0 {
		return nil
	}
	images := make([]string, 0, len(chosen))
	for _, p := range chosen {
		data, err := o.imageBytes(ctx, p)
		if err != nil {
			o.log().Warn("skipping photo for model input", "file", p.Path, "err", err)
			continue
		}
		sent := shrink(data)
		o.log().Debug("model input", "file", p.Path,
			"bytes_before", len(data), "bytes_sent", len(sent))
		images = append(images, base64.StdEncoding.EncodeToString(sent))
	}
	return images
}

// choosePhotos applies the eligibility and sampling rules. Small groups
// (≤ SmallGroupMax) use every eligible photo, extracted previews included; large
// groups prefer already-viewable JPEG/PNG and only extract previews to fill the
// sample size (FR-012), since extraction costs an exiftool process per photo.
func (o *OllamaClassifier) choosePhotos(c photo.Cluster) []photo.Photo {
	twins := decodableTwins(c)
	var direct, extracted []photo.Photo
	for _, p := range c.Photos {
		switch {
		case p.Format.Decodable():
			direct = append(direct, p)
		case p.Format.NeedsPreview() && o.extractorFor(p.Format) != nil:
			// A RAW or HEIC shot alongside its JPEG shows the model the same scene
			// twice and burns a sample slot doing it. The JPEG is already viewable,
			// so the twin is the one to drop.
			if _, twin := twins[twinKey(p.Path)]; twin {
				o.log().Debug("skipping preview of a photo already sent as JPEG/PNG", "file", p.Path)
				continue
			}
			extracted = append(extracted, p)
		}
	}
	eligible := len(direct) + len(extracted)
	if o.Sample <= 0 || eligible == 0 {
		return nil
	}
	// Small group, or few eligible: use every eligible photo, previews included.
	if len(c.Photos) <= SmallGroupMax || eligible <= o.Sample {
		out := make([]photo.Photo, 0, eligible)
		out = append(out, direct...)
		return append(out, extracted...)
	}
	// Large group: prefer JPEG/PNG; extract previews only to fill the sample size.
	if len(direct) >= o.Sample {
		return evenlySpaced(direct, o.Sample)
	}
	out := make([]photo.Photo, 0, o.Sample)
	out = append(out, direct...)
	return append(out, evenlySpaced(extracted, o.Sample-len(direct))...)
}

// decodableTwins indexes the cluster's directly-viewable photos by twinKey, so a
// RAW or HEIC sibling of one can be recognised and skipped.
func decodableTwins(c photo.Cluster) map[string]struct{} {
	twins := make(map[string]struct{})
	for _, p := range c.Photos {
		if p.Format.Decodable() {
			twins[twinKey(p.Path)] = struct{}{}
		}
	}
	return twins
}

// twinKey identifies "the same shot" as a directory plus a base name without its
// extension, case-folded because the pair is often IMG_1234.HEIC + IMG_1234.JPG.
// Two files in different directories are never twins, however alike their names.
func twinKey(path string) string {
	name := filepath.Base(path)
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	return strings.ToLower(filepath.Dir(path) + string(filepath.Separator) + stem)
}

// imageBytes returns the image bytes for a model-eligible photo: the file itself
// for JPEG/PNG, or the exiftool-extracted preview (in memory) for a RAW or HEIC,
// whose pixels no pure-Go decoder can reach.
func (o *OllamaClassifier) imageBytes(ctx context.Context, p photo.Photo) ([]byte, error) {
	if p.Format.NeedsPreview() {
		ex := o.extractorFor(p.Format)
		if ex == nil {
			return nil, fmt.Errorf("no preview extractor configured for %q", p.Path)
		}
		return ex.Extract(ctx, p.Path)
	}
	data, err := os.ReadFile(p.Path)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", p.Path, err)
	}
	return data, nil
}

// extractorFor returns the extractor that can render this format, or nil when
// none is configured for it. RAW and HEIC each need their own program, so having
// one says nothing about having the other.
func (o *OllamaClassifier) extractorFor(f photo.Format) PreviewExtractor {
	switch {
	case f == photo.HEIC:
		return o.HEICPreview
	case f.IsRAW():
		return o.RawPreview
	default:
		return nil
	}
}

// evenlySpaced picks n photos spread across the slice (first … last), so a long
// event is sampled representatively rather than from its start.
func evenlySpaced(photos []photo.Photo, n int) []photo.Photo {
	if n >= len(photos) {
		return photos
	}
	out := make([]photo.Photo, 0, n)
	if n == 1 {
		return append(out, photos[0])
	}
	step := float64(len(photos)-1) / float64(n-1)
	for i := range n {
		idx := int(float64(i)*step + 0.5)
		out = append(out, photos[idx])
	}
	return out
}

// slugifyAnswer reduces raw model output to a slug: its first line, lowercased,
// with runs of non-slug characters collapsed to single hyphens (e.g.
// "Special Events." → "special-events"). It does not validate against any set.
func slugifyAnswer(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugNonWord.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// normaliseTheme slugifies the model output and returns it only if it is one of
// the configured themes, otherwise "".
func normaliseTheme(s string, themes []string) string {
	if slug := slugifyAnswer(s); inSet(slug, themes) {
		return slug
	}
	return ""
}

package classify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
)

// RawExtractor turns a RAW file into model-viewable image bytes (its embedded
// JPEG preview). It is implemented by *rawpreview.Extractor. A nil RawExtractor
// disables RAW input: RAW photos are then skipped for the model (like HEIC).
type RawExtractor interface {
	Extract(ctx context.Context, rawPath string) ([]byte, error)
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
	HTTP    *http.Client
	Logger  *slog.Logger
	Raw     RawExtractor // optional; extracts previews for RAW photos
}

// NewOllama builds an OllamaClassifier with sane defaults for the given themes.
func NewOllama(baseURL, model string, sample int, themes []string) *OllamaClassifier {
	return &OllamaClassifier{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		Sample:  sample,
		Themes:  themes,
		Timeout: 60 * time.Second,
		HTTP:    &http.Client{},
		Logger:  slog.Default(),
	}
}

// log returns the configured logger or the default, never nil.
func (o *OllamaClassifier) log() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return StatusUnreachable
	}

	var tags tagsResponse
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
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
}

type chatResponse struct {
	Message chatMessage `json:"message"`
}

// schemaProperty is one property of a structured-output JSON Schema.
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

// structuredAnswer is the shape the model is asked to return.
type structuredAnswer struct {
	Category string `json:"category"`
}

// slugNonWord matches runs of characters that are not slug-safe.
var slugNonWord = regexp.MustCompile(`[^a-z0-9]+`)

// systemPrompt is the stable output contract sent as the system message. It
// fixes the model's role and the JSON shape; the per-request category list lives
// in userPrompt. Naming JSON here is recommended alongside the Format schema.
func (o *OllamaClassifier) systemPrompt() string {
	return "You are an image classifier. You are shown several photos taken at the same moment. " +
		`Respond ONLY with a JSON object of the form {"category": "<one allowed category>"}. ` +
		"The category MUST be exactly one value from the allowed list, in lowercase, with no extra text."
}

// userPrompt carries the per-request data: the allowed categories and the task.
func (o *OllamaClassifier) userPrompt() string {
	return "Allowed categories: " + strings.Join(o.Themes, ", ") + ". " +
		"If none fits perfectly, choose the closest. " +
		"Classify these photos into exactly one of the allowed categories."
}

// schema constrains the model to answer with exactly one configured theme.
func (o *OllamaClassifier) schema() responseSchema {
	return responseSchema{
		Type: "object",
		Properties: map[string]schemaProperty{
			"category": {Type: "string", Enum: o.Themes},
		},
		Required: []string{"category"},
	}
}

// Classify returns one configured theme slug for the cluster, or an error on
// failure (transport, timeout, or an answer outside the configured set).
func (o *OllamaClassifier) Classify(ctx context.Context, c photo.Cluster) (string, error) {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}

	images := o.sampleImages(ctx, c)
	if len(images) == 0 {
		o.log().Warn("classification skipped: no usable image (HEIC, or RAW without a preview, is not sent to the model)",
			"group_size", len(c.Photos))
		return "", fmt.Errorf("no usable image to classify")
	}
	reqBody := chatRequest{
		Model:  o.Model,
		Stream: false,
		Format: o.schema(),
		Messages: []chatMessage{
			{Role: "system", Content: o.systemPrompt()},
			{Role: "user", Content: o.userPrompt(), Images: images},
		},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("encoding ollama request: %w", err)
	}

	o.log().Debug("contacting model", "url", o.BaseURL, "model", o.Model, "images", len(images))

	const attempts = 2
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		theme, err := o.doChat(ctx, payload)
		if err == nil {
			return theme, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break // timeout/cancel: do not keep retrying
		}
	}
	o.log().Warn("model unavailable or answer rejected — fallback",
		"url", o.BaseURL, "model", o.Model, "err", lastErr)
	return "", fmt.Errorf("ollama unavailable after %d attempts: %w", attempts, lastErr)
}

func (o *OllamaClassifier) doChat(ctx context.Context, payload []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("unreadable ollama response: %w", err)
	}
	// Prefer the structured {"category": "..."} answer; fall back to the raw
	// content for models that ignore the Format schema. normaliseTheme then
	// validates either against the configured set.
	answer := parsed.Message.Content
	var structured structuredAnswer
	if err := json.Unmarshal([]byte(answer), &structured); err == nil && structured.Category != "" {
		answer = structured.Category
	}
	theme := normaliseTheme(answer, o.Themes)
	o.log().Debug("model answer",
		"raw", strings.TrimSpace(parsed.Message.Content), "theme", theme)
	if theme == "" {
		return "", fmt.Errorf("category out of set: %q", strings.TrimSpace(parsed.Message.Content))
	}
	return theme, nil
}

// sampleImages selects the photos to send and returns their base64 content.
// Eligible photos are JPEG/PNG (read directly) or RAW (preview via the extractor);
// HEIC and unknown formats are excluded. A photo whose bytes cannot be obtained
// (read error, or RAW with no usable preview) is skipped, never fatal (FR-007).
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
		images = append(images, base64.StdEncoding.EncodeToString(data))
	}
	return images
}

// choosePhotos applies the eligibility and sampling rules. Small groups
// (≤ SmallGroupMax) use every eligible photo, RAW included; large groups prefer
// already-viewable JPEG/PNG and only extract RAW previews to fill the sample
// size (FR-012).
func (o *OllamaClassifier) choosePhotos(c photo.Cluster) []photo.Photo {
	var direct, raw []photo.Photo
	for _, p := range c.Photos {
		switch {
		case p.Format.Decodable():
			direct = append(direct, p)
		case p.Format.IsRAW() && o.Raw != nil:
			raw = append(raw, p)
		}
	}
	eligible := len(direct) + len(raw)
	if o.Sample <= 0 || eligible == 0 {
		return nil
	}
	// Small group, or few eligible: use every eligible photo (RAW included).
	if len(c.Photos) <= SmallGroupMax || eligible <= o.Sample {
		out := make([]photo.Photo, 0, eligible)
		out = append(out, direct...)
		return append(out, raw...)
	}
	// Large group: prefer JPEG/PNG; extract RAW only to fill the sample size.
	if len(direct) >= o.Sample {
		return evenlySpaced(direct, o.Sample)
	}
	out := make([]photo.Photo, 0, o.Sample)
	out = append(out, direct...)
	return append(out, evenlySpaced(raw, o.Sample-len(direct))...)
}

// imageBytes returns base64-able bytes for a model-eligible photo: the file
// itself for JPEG/PNG, or the exiftool-extracted preview (in memory) for RAW.
func (o *OllamaClassifier) imageBytes(ctx context.Context, p photo.Photo) ([]byte, error) {
	if p.Format.IsRAW() {
		if o.Raw == nil {
			return nil, fmt.Errorf("no RAW extractor configured for %q", p.Path)
		}
		return o.Raw.Extract(ctx, p.Path)
	}
	return os.ReadFile(p.Path)
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
	for i := 0; i < n; i++ {
		idx := int(float64(i)*step + 0.5)
		out = append(out, photos[idx])
	}
	return out
}

// normaliseTheme slugifies the model output and returns it only if it is one of
// the configured themes, otherwise "".
func normaliseTheme(s string, themes []string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugNonWord.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if inSet(s, themes) {
		return s
	}
	return ""
}

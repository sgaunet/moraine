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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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

// baseModel strips an Ollama tag suffix (e.g. "qwen2.5vl:7b" → "qwen2.5vl").
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
}

type chatResponse struct {
	Message chatMessage `json:"message"`
}

// slugNonWord matches runs of characters that are not slug-safe.
var slugNonWord = regexp.MustCompile(`[^a-z0-9]+`)

// prompt instructs the model to answer with exactly one configured theme slug.
// It lists the allowed categories and forbids inventing new ones.
func (o *OllamaClassifier) prompt() string {
	return "Tu classes un ensemble de photos d'un même moment dans UNE SEULE catégorie. " +
		"Catégories autorisées (réponds par l'une EXACTEMENT, en minuscules) : " +
		strings.Join(o.Themes, ", ") + ". " +
		"Si aucune ne convient parfaitement, choisis la plus proche de la liste. " +
		"Réponds par un seul mot de la liste, sans phrase ni ponctuation."
}

// Classify returns one configured theme slug for the cluster, or an error on
// failure (transport, timeout, or an answer outside the configured set).
func (o *OllamaClassifier) Classify(ctx context.Context, c photo.Cluster) (string, error) {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}

	images := sampleImages(c, o.Sample)
	if len(images) == 0 {
		o.log().Warn("classification ignorée : aucune image décodable (HEIC non envoyé au modèle)",
			"taille_groupe", len(c.Photos))
		return "", fmt.Errorf("aucune image décodable à classer")
	}
	reqBody := chatRequest{
		Model:    o.Model,
		Stream:   false,
		Messages: []chatMessage{{Role: "user", Content: o.prompt(), Images: images}},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("encodage requête ollama: %w", err)
	}

	o.log().Debug("contact du modèle", "url", o.BaseURL, "model", o.Model, "images", len(images))

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
	o.log().Warn("modèle indisponible ou réponse rejetée — repli",
		"url", o.BaseURL, "model", o.Model, "err", lastErr)
	return "", fmt.Errorf("ollama indisponible après %d tentatives: %w", attempts, lastErr)
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("statut %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("réponse ollama illisible: %w", err)
	}
	theme := normaliseTheme(parsed.Message.Content, o.Themes)
	if theme == "" {
		return "", fmt.Errorf("catégorie hors ensemble: %q", strings.TrimSpace(parsed.Message.Content))
	}
	return theme, nil
}

// sampleImages selects the photos to send: all decodable photos when the group
// has ≤3 photos, otherwise an evenly-spaced sample of n decodable photos. HEIC
// (non-decodable in pure Go) is skipped. Returns their base64 content.
func sampleImages(c photo.Cluster, n int) []string {
	if n <= 0 {
		return nil
	}
	var decodable []photo.Photo
	for _, p := range c.Photos {
		if p.Format.Decodable() {
			decodable = append(decodable, p)
		}
	}
	if len(decodable) == 0 {
		return nil
	}

	var chosen []photo.Photo
	if len(c.Photos) <= SmallGroupMax || len(decodable) <= n {
		chosen = decodable
	} else {
		chosen = evenlySpaced(decodable, n)
	}

	images := make([]string, 0, len(chosen))
	for _, p := range chosen {
		data, err := os.ReadFile(p.Path)
		if err != nil {
			continue
		}
		images = append(images, base64.StdEncoding.EncodeToString(data))
	}
	return images
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

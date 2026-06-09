package classify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
)

// OllamaClassifier asks a local Ollama vision model for a short category for a
// cluster. Every call is bounded by a context timeout and retried once on a
// transient error (R6). Any failure is the caller's cue to fall back.
type OllamaClassifier struct {
	BaseURL string
	Model   string
	Sample  int
	Timeout time.Duration
	HTTP    *http.Client
}

// NewOllama builds an OllamaClassifier with sane defaults.
func NewOllama(baseURL, model string, sample int) *OllamaClassifier {
	return &OllamaClassifier{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		Sample:  sample,
		Timeout: 60 * time.Second,
		HTTP:    &http.Client{},
	}
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

const classifyPrompt = "Donne une catégorie courte (1 à 3 mots, en français, en minuscules) " +
	"décrivant cet ensemble de photos d'un même moment. Réponds uniquement par la catégorie, sans phrase."

// Classify returns a short category for the cluster, or an error on failure.
func (o *OllamaClassifier) Classify(ctx context.Context, c photo.Cluster) (string, error) {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}

	images := sampleImages(c, o.Sample)
	reqBody := chatRequest{
		Model:    o.Model,
		Stream:   false,
		Messages: []chatMessage{{Role: "user", Content: classifyPrompt, Images: images}},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("encodage requête ollama: %w", err)
	}

	const attempts = 2
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		category, err := o.doChat(ctx, payload)
		if err == nil {
			return category, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break // timeout/cancel: do not keep retrying
		}
	}
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
	category := normaliseCategory(parsed.Message.Content)
	if category == "" {
		return "", fmt.Errorf("catégorie vide renvoyée par le modèle")
	}
	return category, nil
}

// sampleImages reads up to n decodable photos and returns their base64 content.
func sampleImages(c photo.Cluster, n int) []string {
	if n <= 0 {
		return nil
	}
	var images []string
	for _, p := range c.Photos {
		if len(images) >= n {
			break
		}
		if !p.Format.Decodable() {
			continue
		}
		data, err := os.ReadFile(p.Path)
		if err != nil {
			continue
		}
		images = append(images, base64.StdEncoding.EncodeToString(data))
	}
	return images
}

// normaliseCategory trims the model output to a short, clean label.
func normaliseCategory(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\".")
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(strings.ToLower(s))
}

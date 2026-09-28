package classify_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/photo"
)

// goldenThemes is the default theme set plus one custom theme, so the golden prompt
// pins both the hinted and the slug-only rendering of a category.
var goldenThemes = []string{"mountain", "special-events", "cook", "family", "road-trips"}

// goldenCluster carries every fact metadataLines knows how to render: a span, an
// altitude and a location.
func goldenCluster(t *testing.T) photo.Cluster {
	t.Helper()
	c := jpegCluster(t)
	alt := 2710.0
	c.Photos[0].Altitude = &alt
	c.Photos[0].GPS = &photo.LatLng{Lat: 45.9237, Lng: 6.8694}
	c.Start = time.Date(2025, 3, 2, 9, 10, 0, 0, time.UTC)
	c.End = time.Date(2025, 3, 2, 14, 55, 0, 0, time.UTC)
	return c
}

// capturePrompt runs one single-call classification and returns the system and user
// message contents the model received.
func capturePrompt(t *testing.T, oc *classify.OllamaClassifier, c photo.Cluster) string {
	t.Helper()
	var got strings.Builder
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		for _, m := range body.Messages {
			got.WriteString("== " + m.Role + " ==\n" + m.Content + "\n")
		}
		_, _ = w.Write([]byte(`{"message":{"content":"{\"category\":\"mountain\",\"confidence\":0.9}"}}`))
	}))
	defer srv.Close()

	oc.BaseURL = srv.URL
	if _, err := oc.Classify(context.Background(), c); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	return got.String()
}

// TestVisionPromptIsPinned is the FR-002 guard: with no theme descriptions set, the
// single-call prompt must stay byte-identical to what the release before the
// describe-then-decide feature sent. Regenerate the golden file only for a change
// that is meant to alter the prompt: MORAINE_UPDATE_GOLDEN=1 go test ./internal/classify/.
func TestVisionPromptIsPinned(t *testing.T) {
	got := capturePrompt(t, classify.NewOllama("", "m", 3, goldenThemes), goldenCluster(t))

	golden := filepath.Join("testdata", "prompt_default.golden")
	if os.Getenv("MORAINE_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("vision prompt changed:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// An empty override map is no override: the prompt stays the pinned one.
func TestVisionPromptIsPinnedWithAnEmptyOverrideMap(t *testing.T) {
	oc := classify.NewOllama("", "m", 3, goldenThemes)
	oc.ThemeDescriptions = map[string]string{}
	want, err := os.ReadFile(filepath.Join("testdata", "prompt_default.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if got := capturePrompt(t, oc, goldenCluster(t)); got != string(want) {
		t.Errorf("vision prompt changed with an empty override map:\n%s", got)
	}
}

// An override replaces the built-in description in the vision prompt too, and
// describes a custom theme that had none; every other line is left alone.
func TestVisionPromptUsesThemeDescriptions(t *testing.T) {
	oc := classify.NewOllama("", "m", 3, goldenThemes)
	oc.ThemeDescriptions = map[string]string{"cook": "food, market stalls", "road-trips": "driving, roadside stops"}
	got := capturePrompt(t, oc, goldenCluster(t))
	for _, line := range []string{
		"- cook: food, market stalls\n",
		"- road-trips: driving, roadside stops\n",
		"- mountain: mountains, peaks, alpine landscapes, hiking, snow, skiing\n",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("prompt lacks %q:\n%s", line, got)
		}
	}
}

package decide_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/decide"
)

// wireRequest is the systemone body as the server decodes it.
type wireRequest struct {
	Model     string `json:"model"`
	State     string `json:"state"`
	Questions struct {
		Theme struct {
			Type         string            `json:"type"`
			Instructions string            `json:"instructions"`
			Criteria     map[string]string `json:"criteria"`
		} `json:"theme"`
	} `json:"questions"`
}

func decodeRequest(t *testing.T, body []byte) wireRequest {
	t.Helper()
	var req wireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("request body is not the systemone shape: %v\n%s", err, body)
	}
	return req
}

// TestDecisionRequestShape pins the content contracts/decision-request.md fixes, so
// an evaluation run can be reproduced from it.
func TestDecisionRequestShape(t *testing.T) {
	svc, srv := newService(t, nil)
	c := newClient(t, decide.Config{Backend: decide.Laya, URL: srv.URL, Model: "multilingual"})

	d := decision("A snowy ridge with a hiker.", "A plate of pasta on a table.")
	d.Context = []string{
		"taken: 2025-03-02 09:10 to 2025-03-02 14:55 (local time)",
		"highest altitude: 2710 m above sea level",
		"location: 45.92, 6.87",
	}
	d.Options = append(d.Options[:len(d.Options)-1:len(d.Options)-1],
		classify.ThemeOption{Slug: "road-trips"}, classify.ThemeOption{Slug: "other", Description: "the photos fit none of the other themes"})
	if _, err := c.Decide(context.Background(), d); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	body := svc.last(t).body
	req := decodeRequest(t, body)

	wantState := "Photo 1: A snowy ridge with a hiker.\nPhoto 2: A plate of pasta on a table.\n\n" +
		"Context:\n- taken: 2025-03-02 09:10 to 2025-03-02 14:55 (local time)\n" +
		"- highest altitude: 2710 m above sea level\n- location: 45.92, 6.87"
	if req.State != wantState {
		t.Errorf("state =\n%q\nwant\n%q", req.State, wantState)
	}
	if req.Model != "multilingual" {
		t.Errorf("model = %q; want it sent explicitly", req.Model)
	}
	q := req.Questions.Theme
	if q.Type != "choice" {
		t.Errorf("question type = %q; want choice", q.Type)
	}
	if q.Instructions != "These are descriptions of photos taken at one event. Which theme does the event belong to?" {
		t.Errorf("instructions = %q", q.Instructions)
	}
	want := map[string]string{
		"mountain":       "mountains, peaks, alpine landscapes, hiking, snow, skiing",
		"special-events": "weddings, parties, concerts, ceremonies, celebrations",
		"cook":           "food, meals, cooking, plated dishes, restaurants",
		"family":         "people, portraits, family gatherings, children, daily life",
		"road-trips":     "road trips", // an undescribed theme is sent as its slug in words
		"other":          "the photos fit none of the other themes",
	}
	if len(q.Criteria) != len(want) {
		t.Errorf("criteria = %v; want %v", q.Criteria, want)
	}
	for k, v := range want {
		if q.Criteria[k] != v {
			t.Errorf("criteria[%q] = %q; want %q", k, q.Criteria[k], v)
		}
	}
	// Text only: nothing resembling image bytes or a path travels.
	for _, forbidden := range []string{"images", "base64", ".jpg", "/9j/"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("request carries %q:\n%s", forbidden, body)
		}
	}
}

// With no context facts, the state is the photo lines alone.
func TestDecisionRequestOmitsAnEmptyContext(t *testing.T) {
	svc, srv := newService(t, nil)
	c := newClient(t, decide.Config{Backend: decide.Laya, URL: srv.URL, Model: "multilingual"})
	if _, err := c.Decide(context.Background(), decision("A plate of pasta.")); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := decodeRequest(t, svc.last(t).body).State; got != "Photo 1: A plate of pasta." {
		t.Errorf("state = %q; want no Context block", got)
	}
}

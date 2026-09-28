package decide_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/decide"
)

// TestAnswerMapping covers contracts/decision-request.md "Answer → Verdict".
func TestAnswerMapping(t *testing.T) {
	tests := []struct {
		name    string
		backend decide.Backend
		reply   string
		want    classify.Verdict
		wantErr string
	}{
		{"laya reads answer_confidence", decide.Laya,
			`{"answers":{"theme":{"type":"choice","choice":"cook","answer_confidence":0.83,"confidence":0.4}}}`,
			classify.Verdict{Theme: "cook", Confidence: 0.83, Decided: true}, ""},
		{"jev reads confidence", decide.Jev,
			`{"answers":{"theme":{"type":"choice","choice":"cook","answer_confidence":0.83,"confidence":0.4}}}`,
			classify.Verdict{Theme: "cook", Confidence: 0.4, Decided: true}, ""},
		{"missing confidence is unreported", decide.Laya,
			`{"answers":{"theme":{"type":"choice","choice":"cook","confidence":0.4}}}`,
			classify.Verdict{Theme: "cook", Decided: true}, ""},
		{"out-of-range confidence is unreported", decide.Laya,
			`{"answers":{"theme":{"type":"choice","choice":"cook","answer_confidence":83}}}`,
			classify.Verdict{Theme: "cook", Decided: true}, ""},
		{"zero confidence is unreported", decide.Jev,
			`{"answers":{"theme":{"type":"choice","choice":"cook","confidence":0}}}`,
			classify.Verdict{Theme: "cook", Decided: true}, ""},
		{"the fallback is an abstention", decide.Laya,
			`{"answers":{"theme":{"type":"choice","choice":"other","answer_confidence":0.7}}}`,
			classify.Verdict{Confidence: 0.7, Decided: true}, ""},
		{"a choice outside the offered set", decide.Laya,
			`{"answers":{"theme":{"type":"choice","choice":"concert"}}}`,
			classify.Verdict{}, "decision out of set"},
		{"a missing choice", decide.Laya,
			`{"answers":{"theme":{"type":"choice"}}}`,
			classify.Verdict{}, "decision out of set"},
		{"the wrong answer type", decide.Laya,
			`{"answers":{"theme":{"type":"noul","noul":0.5}}}`,
			classify.Verdict{}, "theme"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, srv := newService(t, func(_ int, w http.ResponseWriter) { _, _ = io.WriteString(w, tc.reply) })
			c := newClient(t, decide.Config{Backend: tc.backend, URL: srv.URL, Model: "m", APIKey: "k3y"})
			got, err := c.Decide(context.Background(), decision("A plate of pasta."))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v; want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if got != tc.want {
				t.Errorf("verdict = %+v; want %+v", got, tc.want)
			}
		})
	}
}

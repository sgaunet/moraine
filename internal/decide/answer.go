package decide

import (
	"errors"
	"fmt"
	"slices"

	"github.com/sgaunet/gutcheck"

	"github.com/sgaunet/moraine/internal/classify"
)

// verdictOf maps the service's answer to a Decided verdict. Choosing the fallback is
// an abstention, so the altitude heuristic still gets its say; a choice outside what
// was offered is an error, which sends the event back to the vision model.
func verdictOf(resp *gutcheck.Response, backend Backend, offered []string, fallback string) (classify.Verdict, error) {
	choice, err := resp.Choice(questionID)
	if err != nil {
		var invalid *gutcheck.ResponseValidationError
		if errors.As(err, &invalid) && invalid.FieldPath == "answers."+questionID+".choice" {
			return classify.Verdict{}, fmt.Errorf("decision out of set: no choice in the answer: %w", err)
		}
		return classify.Verdict{}, fmt.Errorf("reading the theme answer: %w", err)
	}
	if !slices.Contains(offered, choice) {
		return classify.Verdict{}, fmt.Errorf("decision out of set: %q", choice)
	}
	v := classify.Verdict{Theme: choice, Confidence: confidenceOf(resp.Answers[questionID], backend), Decided: true}
	if choice == fallback {
		v.Theme = ""
	}
	return v, nil
}

// confidenceOf picks the backend's calibrated number: Laya's answer_confidence (the
// probability of the reported answer; its "confidence" measures how concentrated the
// whole distribution is), Jev's confidence. Absent or outside (0, 1] means "not
// reported", which no threshold rejects.
func confidenceOf(a gutcheck.Answer, backend Backend) float64 {
	c := a.Confidence
	if backend == Laya {
		c = a.AnswerConfidence
	}
	if c == nil || *c <= 0 || *c > 1 {
		return 0
	}
	return *c
}

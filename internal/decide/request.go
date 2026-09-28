package decide

import (
	"strconv"
	"strings"

	"github.com/sgaunet/gutcheck"

	"github.com/sgaunet/moraine/internal/classify"
)

// questionID names the one question a decision asks; the answer comes back under it.
const questionID = "theme"

// instructions is the question itself. It is fixed so an evaluation run can be
// reproduced from contracts/decision-request.md.
const instructions = "These are descriptions of photos taken at one event. Which theme does the event belong to?"

// readinessState is what the readiness check asks about. Its answer is discarded:
// the check is whether the service answers the run's real question at all.
const readinessState = "Photo 1: A test photo."

// buildRequest renders a Decision as the systemone request. The state is plain text,
// not JSON: the checkpoints read prose, and punctuation would only spend tokens.
func buildRequest(d classify.Decision, model string) *gutcheck.Request {
	return request(stateText(d), d.Options, model)
}

// request builds the choice question over options for a given state.
func request(state string, options []classify.ThemeOption, model string) *gutcheck.Request {
	criteria := make(map[string]any, len(options))
	for _, o := range options {
		desc := o.Description
		if desc == "" {
			// Never a null label: an undescribed custom theme is offered as its slug in
			// words, which is what a reader of "road-trips" would take it to mean.
			desc = strings.ReplaceAll(o.Slug, "-", " ")
		}
		criteria[o.Slug] = desc
	}
	return &gutcheck.Request{
		Model:     model,
		State:     state,
		Questions: map[string]gutcheck.Question{questionID: gutcheck.Choice(instructions, criteria)},
	}
}

// stateText lists the descriptions as "Photo i:" lines, then the capture context as a
// "Context:" block when there is any.
func stateText(d classify.Decision) string {
	var b strings.Builder
	for i, desc := range d.Descriptions {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("Photo " + strconv.Itoa(i+1) + ": " + desc)
	}
	if len(d.Context) > 0 {
		b.WriteString("\n\nContext:")
		for _, l := range d.Context {
			b.WriteString("\n- " + l)
		}
	}
	return b.String()
}

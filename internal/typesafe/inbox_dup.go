package typesafe

import (
	"context"
	"fmt"
	"strings"
)

// DupCheck asks whether one capture repeats any of a shortlist of existing
// open tasks. The shortlist comes from code (a cheap lexical match); the model
// only judges the candidates it is shown, so a duplicate missing from the
// shortlist cannot be found.
type DupCheck struct {
	Body       []string
	Candidates []string // open task texts
}

// DupResult is the probability that the capture repeats each candidate,
// index-aligned with DupCheck.Candidates; a missing answer is -1.
type DupResult struct {
	Probabilities []float64
}

func dupQuestionID(i, k int) string { return fmt.Sprintf("dup_%d_%d", i, k) }

// dupRequest builds one request over a batch of checks: one Noul per
// (capture, candidate) pair, all sharing the state.
func dupRequest(checks []DupCheck) Request {
	type item struct {
		Text      string   `json:"text"`
		OpenTasks []string `json:"open_tasks"`
	}
	items := make([]item, len(checks))
	questions := make(map[string]Question)
	for i, c := range checks {
		items[i] = item{Text: strings.Join(c.Body, "\n"), OpenTasks: c.Candidates}
		for k := range c.Candidates {
			questions[dupQuestionID(i, k)] = Question{
				Type: TypeNoul,
				Instructions: map[string]string{
					"question": fmt.Sprintf("Is `captures[%d].text` the same commitment as the existing open task `captures[%d].open_tasks[%d]`?", i, i, k),
					"focus":    "Yes only if acting on one would make the other redundant: the same action on the same thing, even if worded differently or one has more detail. Related work, the same project, or a similar topic is not enough.",
				},
			}
		}
	}
	return Request{
		State: map[string]any{
			"context":  "Quick captures from a personal notes inbox, each with a shortlist of the user's existing open tasks that look similar. Checking whether a capture repeats a task the user already has.",
			"captures": items,
		},
		Questions: questions,
	}
}

// CheckDuplicates judges a batch of checks in ONE request. A transport or API
// error fails the batch.
func (c InboxClassifier) CheckDuplicates(ctx context.Context, checks []DupCheck) ([]DupResult, Usage, error) {
	n := 0
	for _, ch := range checks {
		n += len(ch.Candidates)
	}
	out := make([]DupResult, len(checks))
	if n == 0 {
		return out, Usage{}, nil
	}
	resp, err := c.Client.SystemOne(ctx, dupRequest(checks))
	if err != nil {
		return nil, Usage{}, err
	}
	for i, ch := range checks {
		out[i].Probabilities = make([]float64, len(ch.Candidates))
		for k := range ch.Candidates {
			out[i].Probabilities[k] = -1
			if a, ok := resp.Answers[dupQuestionID(i, k)]; ok && a.Noul != nil {
				out[i].Probabilities[k] = *a.Noul
			}
		}
	}
	return out, resp.Usage, nil
}

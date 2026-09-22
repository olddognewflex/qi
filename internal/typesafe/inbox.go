package typesafe

import (
	"context"
	"fmt"
	"strings"
)

// Inbox triage option keys. They deliberately equal the service layer's
// InboxAction* values so the command-layer adapter maps them verbatim; this
// package does not import service (it stays a leaf, like internal/embed).
const (
	InboxTask    = "task"
	InboxNote    = "note"
	InboxArchive = "archive"
)

// inboxContext frames the captures for the model: whose they are and why they
// are being looked at.
const inboxContext = "A quick capture from a personal notes inbox, written by the user to themselves. It needs triage."

// inboxCriteria are the Choice options shared by every capture's question.
// They describe intent rather than shape, so a terse fragment ("milk") can
// still read as a task and a long line can still read as one.
var inboxCriteria = map[string]string{
	InboxTask:    "A concrete action the user intends to do: a todo, reminder, errand, follow-up, purchase, or call to make, even if phrased tersely or as a fragment.",
	InboxNote:    "Information worth keeping for reference: an idea, observation, fact, quote, link with context, meeting or reading notes. Nothing to do.",
	InboxArchive: "Nothing worth keeping: empty, accidental, a test, gibberish, or content with no lasting value.",
}

// inboxQuestionID is the question id capture i's answer comes back under.
func inboxQuestionID(i int) string { return fmt.Sprintf("action_%d", i) }

// inboxQuestion is the Choice question for capture i. Question ids are not
// shown to the model, so each question names its capture by state path and is
// self-contained.
func inboxQuestion(i int) Question {
	return Question{
		Type: TypeChoice,
		Instructions: map[string]string{
			"question": fmt.Sprintf("What should the user do with `captures[%d].text`?", i),
			"focus":    "Decide by what the user intends, not by length or formatting. Judge only this capture; the others are unrelated.",
		},
		Criteria: inboxCriteria,
	}
}

// InboxClassification is the model's verdict on one capture, as plain values.
type InboxClassification struct {
	Action        string
	Confidence    float64
	Probabilities map[string]float64
}

// InboxResult is one capture's outcome within a batch: a classification, or
// Err when the response carried no usable answer for it.
type InboxResult struct {
	InboxClassification
	Err error
}

// InboxClassifier asks TypeSafe how to triage inbox captures. The capture
// text leaves the machine, which is why the caller only builds one when the
// user opted in.
type InboxClassifier struct {
	Client *Client
}

// inboxRequest builds one System One request over a batch of capture bodies:
// every capture in state.captures and one Choice question per capture.
func inboxRequest(bodies [][]string) Request {
	captures := make([]map[string]string, len(bodies))
	questions := make(map[string]Question, len(bodies))
	for i, body := range bodies {
		captures[i] = map[string]string{"text": strings.Join(body, "\n")}
		questions[inboxQuestionID(i)] = inboxQuestion(i)
	}
	return Request{
		State: map[string]any{
			"context":  inboxContext,
			"captures": captures,
		},
		Questions: questions,
	}
}

// ClassifyInbox classifies a batch of capture bodies (each its non-empty
// content lines) in ONE request; callers chunk to InboxBatchSize. A transport
// or API error fails the whole batch and is returned as error; otherwise the
// results are index-aligned with bodies, and a capture whose answer is
// missing gets a per-item Err.
func (c InboxClassifier) ClassifyInbox(ctx context.Context, bodies [][]string) ([]InboxResult, error) {
	if len(bodies) == 0 {
		return nil, nil
	}
	resp, err := c.Client.SystemOne(ctx, inboxRequest(bodies))
	if err != nil {
		return nil, err
	}
	out := make([]InboxResult, len(bodies))
	for i := range bodies {
		id := inboxQuestionID(i)
		ans, ok := resp.Answers[id]
		if !ok || ans.Choice == "" {
			out[i].Err = fmt.Errorf("typesafe: response has no %q choice answer", id)
			continue
		}
		out[i].InboxClassification = InboxClassification{
			Action:        ans.Choice,
			Confidence:    ans.Confidence,
			Probabilities: ans.Probabilities,
		}
	}
	return out, nil
}

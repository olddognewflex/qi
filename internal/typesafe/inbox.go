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

// inboxQuestionID is the question id the action answer comes back under.
const inboxQuestionID = "action"

// inboxContext frames the capture for the model: whose it is and why it is
// being looked at.
const inboxContext = "A quick capture from a personal notes inbox, written by the user to themselves. It needs triage."

// inboxQuestion is the single Choice question asked per capture. The criteria
// describe intent rather than shape, so a terse fragment ("milk") can still
// read as a task and a long line can still read as one.
var inboxQuestion = Question{
	Type: TypeChoice,
	Instructions: map[string]string{
		"question": "What should the user do with `capture.text`?",
		"focus":    "Decide by what the user intends, not by length or formatting.",
	},
	Criteria: map[string]string{
		InboxTask:    "A concrete action the user intends to do: a todo, reminder, errand, follow-up, purchase, or call to make, even if phrased tersely or as a fragment.",
		InboxNote:    "Information worth keeping for reference: an idea, observation, fact, quote, link with context, meeting or reading notes. Nothing to do.",
		InboxArchive: "Nothing worth keeping: empty, accidental, a test, gibberish, or content with no lasting value.",
	},
}

// InboxClassification is the model's verdict on one capture, as plain values.
type InboxClassification struct {
	Action        string
	Confidence    float64
	Probabilities map[string]float64
}

// InboxClassifier asks TypeSafe how to triage one inbox capture. One request
// per capture; the capture text leaves the machine, which is why the caller
// only builds one when the user opted in.
type InboxClassifier struct {
	Client *Client
}

// inboxRequest builds the System One request for a capture body.
func inboxRequest(body []string) Request {
	return Request{
		State: map[string]any{
			"capture": map[string]string{"text": strings.Join(body, "\n")},
			"context": inboxContext,
		},
		Questions: map[string]Question{inboxQuestionID: inboxQuestion},
	}
}

// ClassifyInbox classifies one capture body (its non-empty content lines).
func (c InboxClassifier) ClassifyInbox(ctx context.Context, body []string) (InboxClassification, error) {
	resp, err := c.Client.SystemOne(ctx, inboxRequest(body))
	if err != nil {
		return InboxClassification{}, err
	}
	ans, ok := resp.Answers[inboxQuestionID]
	if !ok || ans.Choice == "" {
		return InboxClassification{}, fmt.Errorf("typesafe: response has no %q choice answer", inboxQuestionID)
	}
	return InboxClassification{
		Action:        ans.Choice,
		Confidence:    ans.Confidence,
		Probabilities: ans.Probabilities,
	}, nil
}

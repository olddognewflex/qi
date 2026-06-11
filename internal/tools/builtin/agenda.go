package builtin

import (
	"context"
	"encoding/json"

	"qi/internal/domain"
	"qi/internal/service"
	"qi/internal/tools"
)

// AgendaTodayToolName is the registered name of the agenda-today tool.
const AgendaTodayToolName = "agenda.today"

type agendaEvent struct {
	Title   string `json:"title"`
	Start   string `json:"start"`
	End     string `json:"end,omitempty"`
	Project string `json:"project,omitempty"`
}

type agendaTodayResult struct {
	Events   []agendaEvent `json:"events"`
	Warnings []string      `json:"warnings,omitempty"`
}

// RegisterAgendaToday binds agenda.today against the supplied AgendaService.
// Read-only: aggregates today's events from all configured calendar providers.
func RegisterAgendaToday(r *tools.Registry, agenda service.AgendaService) error {
	tool := tools.Tool{
		Name:        AgendaTodayToolName,
		Version:     "1",
		Mutating:    false,
		Description: "List today's calendar events from all configured providers.",
		Schema:      json.RawMessage(`{"type":"object","additionalProperties":false}`),
	}
	handler := func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		events, warnings, err := agenda.Today()
		if err != nil {
			return nil, err
		}
		out := agendaTodayResult{Events: make([]agendaEvent, 0, len(events))}
		for _, e := range events {
			out.Events = append(out.Events, formatAgendaEvent(e))
		}
		for _, w := range warnings {
			out.Warnings = append(out.Warnings, w.Error())
		}
		return json.Marshal(out)
	}
	return r.RegisterLocal(tool, handler)
}

func formatAgendaEvent(e domain.Event) agendaEvent {
	out := agendaEvent{
		Title:   e.Title,
		Start:   e.Start.Format("15:04"),
		Project: e.Project,
	}
	if !e.End.IsZero() && !e.End.Equal(e.Start) {
		out.End = e.End.Format("15:04")
	}
	return out
}

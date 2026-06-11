package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"qi/internal/calendar"
	"qi/internal/domain"
	"qi/internal/service"
	"qi/internal/tools"
)

type fakeProvider struct {
	name   string
	events []domain.Event
	err    error
}

func (p fakeProvider) Name() string { return p.name }
func (p fakeProvider) Events(from, to time.Time) ([]domain.Event, error) {
	return p.events, p.err
}

func TestAgendaTodayRoundtrip(t *testing.T) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location())
	svc := service.AgendaService{Providers: []calendar.Provider{
		fakeProvider{name: "local", events: []domain.Event{
			{Title: "Standup", Start: start, End: start.Add(30 * time.Minute), Project: "work"},
		}},
	}}

	r := tools.NewRegistry()
	if err := RegisterAgendaToday(r, svc); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, ok := r.Lookup(AgendaTodayToolName)
	if !ok {
		t.Fatalf("lookup miss for %s", AgendaTodayToolName)
	}
	if tool.Mutating {
		t.Fatal("agenda.today must be read-only")
	}

	out, err := tools.Execute(context.Background(), r, AgendaTodayToolName, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var res agendaTodayResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(res.Events))
	}
	got := res.Events[0]
	if got.Title != "Standup" || got.Start != "09:00" || got.End != "09:30" || got.Project != "work" {
		t.Fatalf("unexpected event: %+v", got)
	}
}

func TestAgendaTodaySurfacesWarnings(t *testing.T) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location())
	svc := service.AgendaService{Providers: []calendar.Provider{
		fakeProvider{name: "ok", events: []domain.Event{{Title: "Lunch", Start: start}}},
		fakeProvider{name: "broken", err: errors.New("token expired")},
	}}
	r := tools.NewRegistry()
	if err := RegisterAgendaToday(r, svc); err != nil {
		t.Fatalf("register: %v", err)
	}
	out, err := tools.Execute(context.Background(), r, AgendaTodayToolName, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var res agendaTodayResult
	_ = json.Unmarshal(out, &res)
	if len(res.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %v", res.Warnings)
	}
}

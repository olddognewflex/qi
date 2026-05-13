package calendar

import (
	"fmt"
	"net/http"
	"time"

	ical "github.com/emersion/go-ical"
	"qi/internal/domain"
)

type ICSProvider struct {
	CalName string
	URL     string
}

func (p ICSProvider) Name() string { return p.CalName }

func (p ICSProvider) Events(from, to time.Time) ([]domain.Event, error) {
	resp, err := http.Get(p.URL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", p.CalName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", p.CalName, resp.StatusCode)
	}

	dec := ical.NewDecoder(resp.Body)
	cal, err := dec.Decode()
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", p.CalName, err)
	}

	var events []domain.Event
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent {
			continue
		}
		ev, ok := p.toEvent(comp, from, to)
		if ok {
			events = append(events, ev)
		}
	}
	return events, nil
}

func (p ICSProvider) toEvent(comp *ical.Component, from, to time.Time) (domain.Event, bool) {
	startProp := comp.Props.Get(ical.PropDateTimeStart)
	if startProp == nil {
		return domain.Event{}, false
	}

	start, err := startProp.DateTime(time.Local)
	if err != nil {
		return domain.Event{}, false
	}

	var end time.Time
	if endProp := comp.Props.Get(ical.PropDateTimeEnd); endProp != nil {
		end, _ = endProp.DateTime(time.Local)
	}
	if end.IsZero() {
		end = start.Add(time.Hour)
	}

	if start.After(to) || end.Before(from) {
		return domain.Event{}, false
	}

	summary := ""
	if s := comp.Props.Get(ical.PropSummary); s != nil {
		summary = s.Value
	}

	uid := ""
	if u := comp.Props.Get(ical.PropUID); u != nil {
		uid = u.Value
	}

	return domain.Event{
		ID:     uid,
		Source: p.CalName,
		Title:  summary,
		Start:  start,
		End:    end,
	}, true
}

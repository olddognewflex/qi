package service

import (
	"fmt"
	"sort"
	"time"

	"qi/internal/calendar"
	"qi/internal/domain"
)

type AgendaService struct {
	Providers []calendar.Provider
}

// ProviderWarning records a single provider that failed to return events. The
// agenda still renders events from the providers that succeeded; callers print
// these warnings so a dead calendar (e.g. an expired OAuth token) is visible
// instead of silently dropping out of the agenda.
type ProviderWarning struct {
	Provider string
	Err      error
}

func (w ProviderWarning) Error() string {
	return fmt.Sprintf("%s: %v", w.Provider, w.Err)
}

func (s AgendaService) Today() ([]domain.Event, []ProviderWarning, error) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.Add(24 * time.Hour)
	return s.fetch(start, end)
}

func (s AgendaService) Week() ([]domain.Event, []ProviderWarning, error) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 0, 7)
	return s.fetch(start, end)
}

func (s AgendaService) fetch(from, to time.Time) ([]domain.Event, []ProviderWarning, error) {
	var all []domain.Event
	var warnings []ProviderWarning

	for _, p := range s.Providers {
		events, err := p.Events(from, to)
		if err != nil {
			warnings = append(warnings, ProviderWarning{Provider: p.Name(), Err: err})
			continue
		}
		all = append(all, events...)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Start.Before(all[j].Start)
	})

	// Only fatal when every provider failed and produced nothing.
	if len(all) == 0 && len(warnings) > 0 {
		return nil, warnings, warnings[len(warnings)-1]
	}
	return all, warnings, nil
}

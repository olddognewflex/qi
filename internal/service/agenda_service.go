package service

import (
	"sort"
	"time"

	"qi/internal/calendar"
	"qi/internal/domain"
)

type AgendaService struct {
	Providers []calendar.Provider
}

func (s AgendaService) Today() ([]domain.Event, error) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.Add(24 * time.Hour)
	return s.fetch(start, end)
}

func (s AgendaService) Week() ([]domain.Event, error) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 0, 7)
	return s.fetch(start, end)
}

func (s AgendaService) fetch(from, to time.Time) ([]domain.Event, error) {
	var all []domain.Event
	var lastErr error

	for _, p := range s.Providers {
		events, err := p.Events(from, to)
		if err != nil {
			lastErr = err
			continue
		}
		all = append(all, events...)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Start.Before(all[j].Start)
	})

	if len(all) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return all, nil
}

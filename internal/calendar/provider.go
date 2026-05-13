package calendar

import (
	"time"

	"qi/internal/domain"
)

type Provider interface {
	Events(from, to time.Time) ([]domain.Event, error)
	Name() string
}

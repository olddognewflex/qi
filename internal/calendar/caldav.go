package calendar

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	ical "github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"qi/internal/domain"
)

const googleCalDAVEndpoint = "https://apidata.googleusercontent.com/caldav/v2"

type CalDAVProvider struct {
	CalName  string
	Email    string
	Password string
}

func (p CalDAVProvider) Name() string { return p.CalName }

func (p CalDAVProvider) Events(from, to time.Time) ([]domain.Event, error) {
	httpClient := webdav.HTTPClientWithBasicAuth(&http.Client{}, p.Email, p.Password)

	client, err := caldav.NewClient(httpClient, googleCalDAVEndpoint)
	if err != nil {
		return nil, fmt.Errorf("caldav %s: %w", p.CalName, err)
	}

	calPath := fmt.Sprintf("/%s/events", url.PathEscape(p.Email))

	query := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:     ical.CompEvent,
			AllProps: true,
		},
		CompFilter: caldav.CompFilter{
			Name:  ical.CompCalendar,
			Start: from,
			End:   to,
			Comps: []caldav.CompFilter{
				{Name: ical.CompEvent},
			},
		},
	}

	objects, err := client.QueryCalendar(context.Background(), calPath, query)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", p.CalName, err)
	}

	var events []domain.Event
	for _, obj := range objects {
		for _, ev := range obj.Data.Events() {
			de, ok := calDAVEventToDomain(ev, p.CalName)
			if ok {
				events = append(events, de)
			}
		}
	}
	return events, nil
}

func calDAVEventToDomain(ev ical.Event, source string) (domain.Event, bool) {
	start, err := ev.DateTimeStart(time.Local)
	if err != nil {
		return domain.Event{}, false
	}

	end, err := ev.DateTimeEnd(time.Local)
	if err != nil {
		end = start.Add(time.Hour)
	}

	summary, _ := ev.Props.Text(ical.PropSummary)
	uid, _ := ev.Props.Text(ical.PropUID)

	return domain.Event{
		ID:     uid,
		Source: source,
		Title:  summary,
		Start:  start,
		End:    end,
	}, true
}

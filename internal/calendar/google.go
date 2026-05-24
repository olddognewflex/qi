package calendar

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
	"qi/internal/config"
	"qi/internal/domain"
)

const GoogleCalendarScope = calendar.CalendarReadonlyScope

type GoogleProvider struct {
	CalName      string
	Account      string
	CalendarID   string
	OAuthConfig  *oauth2.Config
}

func (p GoogleProvider) Name() string { return p.CalName }

func (p GoogleProvider) Events(from, to time.Time) ([]domain.Event, error) {
	ctx := context.Background()

	svc, err := NewGoogleService(ctx, p.OAuthConfig, p.Account)
	if err != nil {
		return nil, fmt.Errorf("google %s: %w", p.CalName, err)
	}

	calID := p.CalendarID
	if calID == "" {
		calID = "primary"
	}

	res, err := svc.Events.List(calID).
		TimeMin(from.Format(time.RFC3339)).
		TimeMax(to.Format(time.RFC3339)).
		SingleEvents(true).
		OrderBy("startTime").
		MaxResults(2500).
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("list events %s: %w", p.CalName, err)
	}

	events := make([]domain.Event, 0, len(res.Items))
	for _, item := range res.Items {
		ev, ok := googleEventToDomain(item, p.CalName)
		if ok {
			events = append(events, ev)
		}
	}
	return events, nil
}

func googleEventToDomain(item *calendar.Event, source string) (domain.Event, bool) {
	if item.Start == nil {
		return domain.Event{}, false
	}

	start, ok := parseGoogleTime(item.Start)
	if !ok {
		return domain.Event{}, false
	}

	end := start.Add(time.Hour)
	if item.End != nil {
		if e, ok := parseGoogleTime(item.End); ok {
			end = e
		}
	}

	return domain.Event{
		ID:     item.Id,
		Source: source,
		Title:  item.Summary,
		Start:  start,
		End:    end,
	}, true
}

func parseGoogleTime(t *calendar.EventDateTime) (time.Time, bool) {
	if t.DateTime != "" {
		parsed, err := time.Parse(time.RFC3339, t.DateTime)
		if err != nil {
			return time.Time{}, false
		}
		return parsed.Local(), true
	}
	if t.Date != "" {
		parsed, err := time.ParseInLocation("2006-01-02", t.Date, time.Local)
		if err != nil {
			return time.Time{}, false
		}
		return parsed, true
	}
	return time.Time{}, false
}

func NewGoogleService(ctx context.Context, oauthCfg *oauth2.Config, account string) (*calendar.Service, error) {
	tok, err := LoadToken(account)
	if err != nil {
		return nil, fmt.Errorf("no token for account %s (run `qi calendar auth`): %w", account, err)
	}
	ts := &persistingTokenSource{
		account: account,
		base:    oauthCfg.TokenSource(ctx, tok),
		initial: tok,
	}
	return calendar.NewService(ctx, option.WithTokenSource(ts))
}

func ListAuthorizedAccounts() ([]string, error) {
	dir := filepath.Join(config.DataDir(), "tokens")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(name, ".json"))
	}
	return out, nil
}

type persistingTokenSource struct {
	account string
	base    oauth2.TokenSource
	initial *oauth2.Token
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.base.Token()
	if err != nil {
		return nil, err
	}
	if p.initial == nil || tok.AccessToken != p.initial.AccessToken || !tok.Expiry.Equal(p.initial.Expiry) {
		if saveErr := SaveToken(p.account, tok); saveErr == nil {
			p.initial = tok
		}
	}
	return tok, nil
}

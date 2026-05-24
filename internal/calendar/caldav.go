package calendar

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	ical "github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"qi/internal/domain"
)

type debugRoundTripper struct {
	base http.RoundTripper
}

func (d debugRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	dump, _ := httputil.DumpRequestOut(req, true)
	fmt.Fprintf(os.Stderr, "=== REQUEST ===\n%s\n", dump)
	resp, err := d.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Fprintf(os.Stderr, "=== RESPONSE %s ===\n%s\n", resp.Status, string(body))
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

type CalDAVProvider struct {
	CalName  string
	Endpoint string
	Username string
	Password string
	Path     string // empty = discover all calendars under account and merge
}

func (p CalDAVProvider) httpClient() *http.Client {
	transport := http.DefaultTransport
	if os.Getenv("QI_HTTP_TRACE") != "" {
		transport = debugRoundTripper{base: http.DefaultTransport}
	}
	return &http.Client{Transport: transport}
}

func (p CalDAVProvider) absURL(path string) (string, error) {
	base, err := url.Parse(p.Endpoint)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

func (p CalDAVProvider) doRaw(ctx context.Context, method, path string, body string, headers map[string]string) (*http.Response, error) {
	absURL, err := p.absURL(path)
	if err != nil {
		return nil, err
	}
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, absURL, bodyReader)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(p.Username, p.Password)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return p.httpClient().Do(req)
}

type rawMultiStatus struct {
	XMLName   xml.Name      `xml:"DAV: multistatus"`
	Responses []rawResponse `xml:"response"`
}

type rawResponse struct {
	Href string `xml:"href"`
}

func (p CalDAVProvider) propfindHrefs(ctx context.Context, path string) ([]string, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/></D:prop></D:propfind>`
	resp, err := p.doRaw(ctx, "PROPFIND", path, body, map[string]string{
		"Depth":        "1",
		"Content-Type": `application/xml; charset="utf-8"`,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 207 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("propfind %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var ms rawMultiStatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		return nil, fmt.Errorf("parse multistatus: %w", err)
	}
	var hrefs []string
	for _, r := range ms.Responses {
		href := strings.TrimSpace(r.Href)
		if href == "" || href == path || strings.HasSuffix(href, "/") {
			continue
		}
		hrefs = append(hrefs, href)
	}
	return hrefs, nil
}

func (p CalDAVProvider) fetchICS(ctx context.Context, href string) (*ical.Calendar, error) {
	resp, err := p.doRaw(ctx, "GET", href, "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get %s: %s: %s", href, resp.Status, strings.TrimSpace(string(b)))
	}
	return ical.NewDecoder(resp.Body).Decode()
}

func (p CalDAVProvider) Name() string { return p.CalName }

func (p CalDAVProvider) newClient() (*caldav.Client, error) {
	transport := http.DefaultTransport
	if os.Getenv("QI_HTTP_TRACE") != "" {
		transport = debugRoundTripper{base: http.DefaultTransport}
	}
	httpClient := webdav.HTTPClientWithBasicAuth(&http.Client{Transport: transport}, p.Username, p.Password)
	return caldav.NewClient(httpClient, p.Endpoint)
}

func (p CalDAVProvider) Events(from, to time.Time) ([]domain.Event, error) {
	ctx := context.Background()

	client, err := p.newClient()
	if err != nil {
		return nil, fmt.Errorf("caldav %s: %w", p.CalName, err)
	}

	paths := []string{p.Path}
	calNames := map[string]string{}
	if p.Path == "" {
		discovered, err := p.discoverCalendars(ctx, client)
		if err != nil {
			return nil, fmt.Errorf("caldav %s discover: %w", p.CalName, err)
		}
		paths = paths[:0]
		for _, c := range discovered {
			paths = append(paths, c.Path)
			calNames[c.Path] = c.Name
		}
	}

	debug := os.Getenv("QI_DEBUG") != ""
	var events []domain.Event
	for _, path := range paths {
		source := p.CalName
		if sub, ok := calNames[path]; ok && sub != "" {
			source = p.CalName + "/" + sub
		}
		evs, err := p.queryPath(ctx, client, path, from, to, source)
		if err != nil {
			if debug {
				fmt.Fprintf(os.Stderr, "[qi] caldav %s query %s failed: %v\n", p.CalName, path, err)
			}
			continue
		}
		events = append(events, evs...)
	}
	return events, nil
}

func (p CalDAVProvider) discoverCalendars(ctx context.Context, client *caldav.Client) ([]caldav.Calendar, error) {
	principal, err := client.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, fmt.Errorf("principal: %w", err)
	}
	homeSet, err := client.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, fmt.Errorf("calendar-home-set: %w", err)
	}
	return client.FindCalendars(ctx, homeSet)
}

func (p CalDAVProvider) queryPath(ctx context.Context, client *caldav.Client, path string, from, to time.Time, source string) ([]domain.Event, error) {
	// iCloud rejects go-webdav's strict REPORT/PROPFIND requests (per-prop 404 is treated
	// as fatal by the library). Bypass with raw PROPFIND for hrefs + GET each .ics, then
	// filter client-side.
	hrefs, err := p.propfindHrefs(ctx, path)
	if err != nil {
		return nil, err
	}
	if len(hrefs) == 0 {
		return nil, nil
	}

	type fetchResult struct {
		cal *ical.Calendar
		err error
	}
	const concurrency = 8
	sem := make(chan struct{}, concurrency)
	results := make([]fetchResult, len(hrefs))
	done := make(chan int, len(hrefs))

	for i, href := range hrefs {
		i, href := i, href
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; done <- i }()
			cal, err := p.fetchICS(ctx, href)
			results[i] = fetchResult{cal: cal, err: err}
		}()
	}
	for range hrefs {
		<-done
	}

	debug := os.Getenv("QI_DEBUG") != ""
	var events []domain.Event
	var rawCount, includedCount int
	var firstErr error
	for _, r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		if r.cal == nil {
			continue
		}
		for _, ev := range r.cal.Events() {
			rawCount++
			expanded := expandEventOccurrences(ev, source, from, to)
			includedCount += len(expanded)
			events = append(events, expanded...)
		}
	}
	if debug {
		fmt.Fprintf(os.Stderr, "[qi] caldav %s %s: hrefs=%d raw=%d included=%d firstErr=%v\n",
			p.CalName, path, len(hrefs), rawCount, includedCount, firstErr)
	}
	return events, nil
}

func expandEventOccurrences(ev ical.Event, source string, from, to time.Time) []domain.Event {
	base, ok := calDAVEventToDomain(ev, source)
	if !ok {
		return nil
	}

	set, err := ev.RecurrenceSet(time.Local)
	if err != nil || set == nil {
		if base.End.Before(from) || base.Start.After(to) {
			return nil
		}
		return []domain.Event{base}
	}

	duration := base.End.Sub(base.Start)
	if duration <= 0 {
		duration = time.Hour
	}

	starts := set.Between(from.Add(-duration), to, true)
	out := make([]domain.Event, 0, len(starts))
	for _, start := range starts {
		out = append(out, domain.Event{
			ID:     base.ID,
			Source: source,
			Title:  base.Title,
			Start:  start.In(time.Local),
			End:    start.In(time.Local).Add(duration),
		})
	}
	return out
}

func (p CalDAVProvider) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	client, err := p.newClient()
	if err != nil {
		return nil, err
	}
	return p.discoverCalendars(ctx, client)
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

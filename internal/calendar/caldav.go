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
	"sort"
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
		rawCount += len(r.cal.Events())
		expanded := expandCalendarEvents(r.cal, source, from, to)
		includedCount += len(expanded)
		events = append(events, expanded...)
	}
	if debug {
		fmt.Fprintf(os.Stderr, "[qi] caldav %s %s: hrefs=%d raw=%d included=%d firstErr=%v\n",
			p.CalName, path, len(hrefs), rawCount, includedCount, firstErr)
	}
	return events, nil
}

// eventGroup is one logical event: a base component plus the RECURRENCE-ID
// components that modify individual instances of it.
type eventGroup struct {
	uid       string
	base      *ical.Event
	overrides []ical.Event
}

// groupCalendarEvents partitions a calendar object's VEVENTs into logical events.
//
// A recurring event with exceptions is stored as several components sharing one
// UID: a base component carrying the RRULE, plus one component per modified
// instance carrying a RECURRENCE-ID naming the slot it replaces (RFC 5545 §3.8.4.4).
//
// Grouping is strictly by a *non-empty* UID. A UID-less component is given its own
// group: it cannot be the target of an override, and merging UID-less components on
// their shared empty UID would discard all but the first. Likewise a second base for
// one UID (not spec-legal) becomes its own group rather than being dropped — a
// component may be reshaped by an override, never silently discarded.
func groupCalendarEvents(cal *ical.Calendar) []*eventGroup {
	var groups []*eventGroup
	byUID := map[string]*eventGroup{}

	for _, ev := range cal.Events() {
		ev := ev
		uid, _ := ev.Props.Text(ical.PropUID)
		isOverride := ev.Props.Get(ical.PropRecurrenceID) != nil

		if uid == "" {
			g := &eventGroup{}
			if isOverride {
				g.overrides = append(g.overrides, ev)
			} else {
				g.base = &ev
			}
			groups = append(groups, g)
			continue
		}

		g, ok := byUID[uid]
		if !ok {
			g = &eventGroup{uid: uid}
			byUID[uid] = g
			groups = append(groups, g)
		}
		switch {
		case isOverride:
			g.overrides = append(g.overrides, ev)
		case g.base == nil:
			g.base = &ev
		default:
			groups = append(groups, &eventGroup{uid: uid, base: &ev})
		}
	}
	return groups
}

// expandCalendarEvents turns every VEVENT in a calendar object into the concrete
// occurrences that fall in [from, to], applying RECURRENCE-ID overrides to the
// recurrence set they modify rather than emitting both the generated occurrence and
// its override.
func expandCalendarEvents(cal *ical.Calendar, source string, from, to time.Time) []domain.Event {
	var events []domain.Event
	for _, g := range groupCalendarEvents(cal) {
		events = append(events, expandGroup(g, source, from, to)...)
	}
	return events
}

// expandGroup expands one logical event: the base's recurrence set, with its
// overrides folded in.
//
// Overrides are applied in two passes because the passes have different precedence.
// RANGE=THISANDFUTURE reshapes a run of slots in place (keeping each slot's identity),
// so a single-instance override naming a slot inside that run still finds it and wins,
// which is the precedence RFC 5545 §3.8.4.4 requires.
//
// Matching is by slot identity, never by wall-clock start: an override's RECURRENCE-ID
// names the slot the base *generated*, and comparing against a start would also match
// an unrelated event another override had already relocated onto that time.
func expandGroup(g *eventGroup, source string, from, to time.Time) []domain.Event {
	if g.base == nil {
		// No base in this object to reshape (it lives in another one, or the object is
		// override-only). Every override, whatever its RANGE, can only render as itself
		// — a THISANDFUTURE with no slots to shift would otherwise vanish.
		var added []domain.Event
		for _, ov := range g.overrides {
			_, added = applySingleOverride(nil, added, ov, g.uid, source)
		}
		return filterEventRange(added, from, to)
	}

	slots := expandEventOccurrences(*g.base, source, from, to)

	thisAndFuture, single := splitOverrides(g.overrides)
	for _, ov := range thisAndFuture {
		slots = applyThisAndFuture(slots, ov, source)
	}

	var added []domain.Event
	for _, ov := range single {
		slots, added = applySingleOverride(slots, added, ov, g.uid, source)
	}

	// Re-check the window at the end: an override can move an instance out of it, and
	// expandEventOccurrences only filtered the slots the base itself generated.
	return filterEventRange(append(slots, added...), from, to)
}

// splitOverrides separates RANGE=THISANDFUTURE overrides from single-instance ones,
// returning the former sorted by RECURRENCE-ID so overlapping runs compose in order.
func splitOverrides(overrides []ical.Event) (thisAndFuture, single []ical.Event) {
	for _, ov := range overrides {
		ridProp := ov.Props.Get(ical.PropRecurrenceID)
		if ridProp == nil {
			continue
		}
		if strings.EqualFold(ridProp.Params.Get(ical.ParamRange), "THISANDFUTURE") {
			thisAndFuture = append(thisAndFuture, ov)
			continue
		}
		single = append(single, ov)
	}
	sort.SliceStable(thisAndFuture, func(i, j int) bool {
		a, aok := recurrenceID(thisAndFuture[i])
		b, bok := recurrenceID(thisAndFuture[j])
		if !aok || !bok {
			return false
		}
		return a.Before(b)
	})
	return thisAndFuture, single
}

// applyThisAndFuture applies a RANGE=THISANDFUTURE override: the named instance and
// every later one are shifted by the override's offset from the slot it names and take
// its summary. Slots are modified in place so they keep their identity.
//
// Limitation: only the time offset, duration and summary are propagated. An override
// that carries its own RRULE (i.e. changes the recurrence *rule* from that point on,
// rather than moving the instances the original rule generates) is NOT re-expanded —
// its rule is ignored and the base rule's slots are shifted instead. Apple/iCloud's
// "change this and all future events" emits the shift form handled here; a rule change
// is rarer and would need a full re-expansion from the override's DTSTART.
func applyThisAndFuture(slots []domain.Event, ov ical.Event, source string) []domain.Event {
	rid, ok := recurrenceID(ov)
	if !ok {
		return slots
	}

	if isCancelled(ov) {
		kept := slots[:0]
		for _, s := range slots {
			if !s.Start.Before(rid) {
				continue
			}
			kept = append(kept, s)
		}
		return kept
	}

	ev, ok := calDAVEventToDomain(ov, source)
	if !ok {
		return slots
	}
	delta := ev.Start.Sub(rid)
	duration := ev.End.Sub(ev.Start)
	if duration <= 0 {
		duration = time.Hour
	}

	for i, s := range slots {
		if s.Start.Before(rid) {
			continue
		}
		start := s.Start.Add(delta)
		slots[i].Start = start
		slots[i].End = start.Add(duration)
		slots[i].Title = ev.Title
	}
	return slots
}

// applySingleOverride substitutes one modified instance: the slot it names is dropped
// and the override's own event takes its place, unless the instance is cancelled.
//
// The replacement is appended even when no slot matched — the named slot may sit
// outside [from, to] while the override itself moved into it, and an override whose
// base component is absent from the object still has to render.
func applySingleOverride(slots, added []domain.Event, ov ical.Event, uid, source string) ([]domain.Event, []domain.Event) {
	rid, ok := recurrenceID(ov)
	if !ok {
		return slots, added
	}
	slotID := occurrenceID(uid, rid)

	kept := slots[:0]
	for _, s := range slots {
		if s.ID == slotID {
			continue
		}
		kept = append(kept, s)
	}
	slots = kept

	if isCancelled(ov) {
		return slots, added
	}
	ev, ok := calDAVEventToDomain(ov, source)
	if !ok {
		return slots, added
	}
	// Identify the instance by the slot it replaces, so an override keeps a stable id
	// no matter where in time it is moved to.
	ev.ID = slotID
	return slots, append(added, ev)
}

// recurrenceID reads an override's RECURRENCE-ID as an instant.
func recurrenceID(ov ical.Event) (time.Time, bool) {
	ridProp := ov.Props.Get(ical.PropRecurrenceID)
	if ridProp == nil {
		return time.Time{}, false
	}
	rid, err := ridProp.DateTime(time.Local)
	if err != nil {
		return time.Time{}, false
	}
	// Cosmetic only: slot matching and ordering compare instants (time.Time.Equal /
	// Before), which are zone-independent. This just keeps a rendered id or log line in
	// the same zone as the ids the base expansion builds.
	return rid.In(time.Local), true
}

func isCancelled(ev ical.Event) bool {
	status, err := ev.Status()
	return err == nil && status == ical.EventCancelled
}

func filterEventRange(events []domain.Event, from, to time.Time) []domain.Event {
	out := events[:0]
	for _, e := range events {
		if e.End.Before(from) || e.Start.After(to) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// expandEventOccurrences expands a single component's recurrence set. It does not
// know about RECURRENCE-ID overrides — use expandCalendarEvents, which applies them.
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
		// Cosmetic: renders the id and the event in the viewer's zone. Comparisons
		// against these times are instant-based and zone-independent.
		start = start.In(time.Local)
		out = append(out, domain.Event{
			// Occurrences of one recurring event share a UID, so qualify the id with
			// the start to keep expanded instances distinguishable.
			ID:     occurrenceID(base.ID, start),
			Source: source,
			Title:  base.Title,
			Start:  start,
			End:    start.Add(duration),
		})
	}
	return out
}

// occurrenceID identifies the recurrence slot starting at start. It makes
// domain.Event.ID heterogeneous by design: a non-recurring event keeps its bare UID,
// while an occurrence gets "<uid>-<RFC3339 slot start>". The id therefore encodes slot
// identity, which is what lets an override be matched to the instance it replaces —
// treat it as an opaque key, not as a UID. A UID-less component has no stable identity
// to build on, so its occurrences carry an empty id (as they did before expansion was
// slot-aware); nothing keys on Event.ID today.
func occurrenceID(uid string, start time.Time) string {
	if uid == "" {
		return ""
	}
	return uid + "-" + start.Format(time.RFC3339)
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

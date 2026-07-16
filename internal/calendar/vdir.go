package calendar

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ical "github.com/emersion/go-ical"
	"qi/internal/domain"
)

// VdirProvider reads a vdir collection: a flat directory holding one .ics file per
// event, plus optional metadata files (displayname/color/order).
// See https://vdirsyncer.readthedocs.io/en/stable/vdir.html
//
// Read-only by design — vdirsyncer owns the CalDAV/Google transport and the writes;
// qi just reads the files it leaves behind, so the agenda works offline and
// interops with khal.
type VdirProvider struct {
	CalName string
	Path    string
}

func (p VdirProvider) Name() string { return p.CalName }

func (p VdirProvider) Events(from, to time.Time) ([]domain.Event, error) {
	entries, err := os.ReadDir(p.Path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p.CalName, err)
	}

	var events []domain.Event
	var fileCount, skipped int
	var firstErr error
	for _, entry := range entries {
		// A collection is flat: no recursion. Skipping anything that is not a .ics
		// also skips the metadata files and the .tmp files vdirsyncer writes
		// transiently before renaming them into place.
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".ics") {
			continue
		}
		fileCount++
		cal, err := decodeICSFile(filepath.Join(p.Path, entry.Name()))
		if err != nil {
			// One unreadable file must not sink the whole collection.
			skipped++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		events = append(events, expandCalendarEvents(cal, p.CalName, from, to)...)
	}
	// Skipping is silent tolerance, so say so: an unreadable collection (bad
	// permissions, a half-synced dir) would otherwise render as an empty day.
	if skipped > 0 || os.Getenv("QI_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[qi] vdir %s: files=%d skipped=%d events=%d firstErr=%v\n",
			p.CalName, fileCount, skipped, len(events), firstErr)
	}
	return events, nil
}

func decodeICSFile(path string) (*ical.Calendar, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ical.NewDecoder(f).Decode()
}

// DiscoverVdir builds one provider per collection directory under root, mirroring
// khal's `type = discover`. Each collection is named by its displayname metadata
// file, falling back to the directory name.
func DiscoverVdir(root string) ([]Provider, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("discover vdir %s: %w", root, err)
	}

	var providers []Provider
	for _, entry := range entries {
		// Skip dot-dirs: a root under version control or holding vdirsyncer's own
		// metadata would otherwise yield ".git" as a zero-event calendar.
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		providers = append(providers, VdirProvider{
			CalName: vdirDisplayName(dir, entry.Name()),
			Path:    dir,
		})
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name() < providers[j].Name() })
	return providers, nil
}

// vdirDisplayName reads a collection's displayname metadata file, falling back to
// the given name when it is absent or empty.
func vdirDisplayName(dir, fallback string) string {
	data, err := os.ReadFile(filepath.Join(dir, "displayname"))
	if err != nil {
		return fallback
	}
	if name := strings.TrimSpace(string(data)); name != "" {
		return name
	}
	return fallback
}

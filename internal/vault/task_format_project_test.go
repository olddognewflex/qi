package vault

import (
	"reflect"
	"testing"

	"qi/internal/domain"
)

func TestFormatTaskLinePreservesExplicitProject(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		tags []string
		want string
	}{
		{"inline tag", "Fix login #urgent", nil, "- [ ] Fix login #portal #urgent"},
		{"project already later", "Fix login #urgent #portal", nil, "- [ ] Fix login #portal #urgent"},
		{"project inside prose", "Discuss #urgent with #portal team", nil, "- [ ] Discuss #portal #urgent with team"},
		{"structured tags", "Fix login", []string{"urgent", "portal"}, "- [ ] Fix login #portal #urgent"},
		{"project already first", "Fix #portal login #urgent", []string{"portal", "urgent"}, "- [ ] Fix #portal login #urgent"},
		{"numeric reference", "Fix issue #94 #urgent", nil, "- [ ] Fix issue #94 #portal #urgent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, err := FormatTaskLine(domain.Task{Text: tc.text, Project: "portal", Tags: tc.tags})
			if err != nil {
				t.Fatal(err)
			}
			if line != tc.want {
				t.Errorf("line = %q, want %q", line, tc.want)
			}
			parsed, ok, err := ParseTaskLine(line)
			if err != nil || !ok {
				t.Fatalf("parse: ok=%v err=%v", ok, err)
			}
			if parsed.Project != "portal" || !reflect.DeepEqual(parsed.Tags, []string{"portal", "urgent"}) {
				t.Errorf("round trip: project=%q tags=%v", parsed.Project, parsed.Tags)
			}
			again, err := FormatTaskLine(parsed)
			if err != nil {
				t.Fatal(err)
			}
			if again != line {
				t.Errorf("format is not stable: %q -> %q", line, again)
			}
		})
	}
}

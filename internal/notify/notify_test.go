package notify

import "testing"

func TestAppleEscape(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`plain`, `plain`},
		{`with "quotes"`, `with \"quotes\"`},
		{`back\slash`, `back\\slash`},
		{"line1\nline2", "line1 line2"},
		{"tab\there", "tab here"},
		{"carriage\rreturn", "carriage return"},
		{`"); do shell script "rm -rf"`, `\"); do shell script \"rm -rf\"`},
	}
	for _, c := range cases {
		if got := appleEscape(c.in); got != c.want {
			t.Errorf("appleEscape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildAppleScript(t *testing.T) {
	got := buildAppleScript("qi — Due today", `Buy "milk"`)
	want := `display notification "Buy \"milk\"" with title "qi — Due today"`
	if got != want {
		t.Errorf("buildAppleScript = %q, want %q", got, want)
	}
}

package domain

import "testing"

func TestIsTag(t *testing.T) {
	cases := map[string]bool{
		"home": true, "q3": true, "v2": true, "14a": true, "a-1": true,
		"work/clientA": true, "_": true, "2026-01": true,
		"14": false, "2026": false, "0": false, "": false,
		"bad tag": false, "a.b": false,
	}
	for in, want := range cases {
		if got := IsTag(in); got != want {
			t.Errorf("IsTag(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsNumericTag(t *testing.T) {
	cases := map[string]bool{
		"14": true, "2026": true, "0": true,
		"": false, "q3": false, "14a": false, "a-1": false, "1-2": false,
	}
	for in, want := range cases {
		if got := IsNumericTag(in); got != want {
			t.Errorf("IsNumericTag(%q) = %v, want %v", in, got, want)
		}
	}
}

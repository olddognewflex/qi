package notify

import (
	"testing"
	"time"
)

func TestNextFire(t *testing.T) {
	loc := time.Local
	cases := []struct {
		name      string
		now       time.Time
		hour, min int
		want      time.Time
	}{
		{
			name: "before at-time today -> today",
			now:  time.Date(2026, 6, 11, 6, 0, 0, 0, loc),
			hour: 8, min: 0,
			want: time.Date(2026, 6, 11, 8, 0, 0, 0, loc),
		},
		{
			name: "after at-time today -> tomorrow",
			now:  time.Date(2026, 6, 11, 9, 0, 0, 0, loc),
			hour: 8, min: 0,
			want: time.Date(2026, 6, 12, 8, 0, 0, 0, loc),
		},
		{
			name: "exactly at boundary -> tomorrow (strictly after)",
			now:  time.Date(2026, 6, 11, 8, 0, 0, 0, loc),
			hour: 8, min: 0,
			want: time.Date(2026, 6, 12, 8, 0, 0, 0, loc),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nextFire(c.now, c.hour, c.min)
			if !got.Equal(c.want) {
				t.Errorf("nextFire(%v, %d, %d) = %v, want %v", c.now, c.hour, c.min, got, c.want)
			}
		})
	}
}

func TestParseHHMM(t *testing.T) {
	valid := map[string][2]int{
		"08:00": {8, 0},
		"00:00": {0, 0},
		"23:59": {23, 59},
		"07:30": {7, 30},
	}
	for in, want := range valid {
		h, m, err := parseHHMM(in)
		if err != nil {
			t.Errorf("parseHHMM(%q) error: %v", in, err)
			continue
		}
		if h != want[0] || m != want[1] {
			t.Errorf("parseHHMM(%q) = %d:%d, want %d:%d", in, h, m, want[0], want[1])
		}
	}

	invalid := []string{"8:00", "abc", "25:00", "08:60", "08", "08:00:00", "", "-1:00", "08:0a"}
	for _, in := range invalid {
		if _, _, err := parseHHMM(in); err == nil {
			t.Errorf("parseHHMM(%q) = nil error, want error", in)
		}
	}
}

func TestNew_Validation(t *testing.T) {
	if _, err := New(Options{OnFire: nil}); err == nil {
		t.Error("New with nil OnFire should error")
	}
	if _, err := New(Options{OnFire: func() {}, At: "99:99"}); err == nil {
		t.Error("New with bad At should error")
	}
	if _, err := New(Options{OnFire: func() {}}); err != nil {
		t.Errorf("New with empty At should default, got error: %v", err)
	}
	s, err := New(Options{OnFire: func() {}, At: "07:30"})
	if err != nil {
		t.Fatal(err)
	}
	if s.hour != 7 || s.min != 30 {
		t.Errorf("New parsed At = %d:%d, want 7:30", s.hour, s.min)
	}
}

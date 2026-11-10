package fact

import (
	"strings"
	"testing"
)

// The marks that change the order text is drawn in can make a line read
// differently from what it says: "run make test" could be shown as something
// else entirely. None of them survives in anything Holdfast keeps as a line.
func TestCleanLineDropsTheMarksThatReorderText(t *testing.T) {
	marks := map[string]string{
		"a left-to-right embedding":  "\u202a",
		"a right-to-left embedding":  "\u202b",
		"a pop directional format":   "\u202c",
		"a left-to-right override":   "\u202d",
		"a right-to-left override":   "\u202e",
		"a left-to-right isolate":    "\u2066",
		"a right-to-left isolate":    "\u2067",
		"a first-strong isolate":     "\u2068",
		"a pop directional isolate":  "\u2069",
		"an override and an isolate": "\u202e\u2066",
	}
	for name, m := range marks {
		for _, r := range m {
			if !Reorders(r) {
				t.Errorf("%s: Reorders(%U) = false", name, r)
			}
		}
		in := "run " + m + "make test" + m + " now"
		if got := CleanLine(in, 80); got != "run make test now" {
			t.Errorf("%s: CleanLine(%q) = %q", name, in, got)
		}
	}

	// The characters next to them in the table are ordinary and stay.
	for _, r := range "\u2029\u202f\u2065\u206a" {
		if Reorders(r) {
			t.Errorf("Reorders(%U) = true, and it is not one of the marks", r)
		}
	}
	if got := CleanLine("10\u202fkm", 80); got != "10 km" {
		t.Errorf("a narrow no-break space is white space: got %q", got)
	}
}

// A line a person may paste into a shell is given whole or not at all: a
// command that lost its end, or a character, is a different command.
func TestWholeLine(t *testing.T) {
	long := "holdfast allow cmd " + strings.Repeat("x", 300)
	for _, tc := range []struct {
		name, in string
		max      int
		want     string
	}{
		{"a clean line", "holdfast allow net example.com", 300, "holdfast allow net example.com"},
		{"exactly as long as allowed", "holdfast", 8, "holdfast"},
		{"one rune too long", "holdfast!", 8, ""},
		{"far too long", long, 300, ""},
		{"two lines", "holdfast allow net example.com\nrm notes", 300, ""},
		{"a doubled space", "holdfast allow  net example.com", 300, ""},
		{"a space at the end", "holdfast allow net example.com ", 300, ""},
		{"an escape", "holdfast allow net \x1b[2Kexample.com", 300, ""},
		{"a mark that reorders", "holdfast allow cmd \u202eecho", 300, ""},
		{"nothing", "", 300, ""},
	} {
		if got := WholeLine(tc.in, tc.max); got != tc.want {
			t.Errorf("%s: WholeLine(%q, %d) = %q, want %q", tc.name, tc.in, tc.max, got, tc.want)
		}
	}
}

package fact

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// A "matches" pattern is read line by line, the way grep reads one.
func TestPatternIsLineByLine(t *testing.T) {
	makefile := "all: build\n\nbuild:\n\tgo build ./...\n\nrelease: build\n\t./ship\n"
	tests := []struct {
		expr, text string
		want       bool
	}{
		{"^release:", makefile, true},
		{"^all:", makefile, true},
		{"^build:$", makefile, true},
		{`\./\.\.\.$`, makefile, true},
		{"^ship", makefile, false}, // the line starts with a tab
		{`^\t\./ship$`, makefile, true},
		{"^release:$", makefile, false},
		{"^$", makefile, true}, // an empty line
		{"^$", "a\nb\n", true}, // the end of the text after the last newline
		{"^$", "a\nb", false},
		{"^b$", "a\nb", true},
		{"^a$", "a", true},
		{"a$", "a\r\n", false}, // a carriage return is part of the line, as it is for grep

		// The whole file can still be meant, with \A and \z or by switching the mode off.
		{`\Aall:`, makefile, true},
		{`\Arelease:`, makefile, false},
		{`ship\n\z`, makefile, true},
		{`build\z`, makefile, false},
		{"(?-m)^release:", makefile, false},
		{"(?-m)^all:", makefile, true},

		// Other flags the pattern sets itself still work, and . still stops at a newline.
		{"(?i)^RELEASE:", makefile, true},
		{"^all:.*release", makefile, false},
		{"(?s)^all:.*release", makefile, true},
	}
	for _, tt := range tests {
		re, err := Pattern(tt.expr)
		if err != nil {
			t.Errorf("Pattern(%q): %v", tt.expr, err)
			continue
		}
		if got := re.MatchString(tt.text); got != tt.want {
			t.Errorf("Pattern(%q) on %q = %v, want %v", tt.expr, tt.text, got, tt.want)
		}
	}
}

// The line mode is added when the pattern is compiled. It is never stored,
// shown or reported.
func TestPatternIsKeptAsWritten(t *testing.T) {
	for _, line := range []string{
		"matches Makefile ^release:",
		`matches go.mod "^go 1\\.\\d+$"`,
		"not matches Makefile (?m)^release:", // written by the person: theirs to keep
		"matches Makefile (?-m)^all:",
	} {
		c, err := ParseCheck(line)
		if err != nil {
			t.Errorf("ParseCheck(%s): %v", line, err)
			continue
		}
		args, _ := Split(line)
		if want := args[len(args)-1]; c.Regex != want {
			t.Errorf("ParseCheck(%s): the pattern is stored as %q, want %q", line, c.Regex, want)
		}
		if got := c.String(); got != line {
			t.Errorf("String() = %s, want %s", got, line)
		}
		if strings.Contains(c.Short(), "(?m)") != strings.Contains(line, "(?m)") {
			t.Errorf("Short() = %s for %s", c.Short(), line)
		}
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var back Check
		if err := json.Unmarshal(raw, &back); err != nil || back.Regex != c.Regex {
			t.Errorf("stored as %s, read back as %q, %v", raw, back.Regex, err)
		}
	}
}

func TestPatternErrors(t *testing.T) {
	// Exactly the patterns the regexp package accepts are accepted, and a
	// mistake is reported against the pattern as it was written.
	for _, expr := range []string{
		"*a", "?a", "{2}", "+", "(", ")", `\`, "[", "a{1001}", "(?z)", "(?m", "a**", `\8`, "(a", "a)", "(?P<n>a)(?P<n>b)", "\xff",
		"|", "", "(?i)", "(?-m)^a$", "x*", "(?s).", "a{2,1}", `\pZ`, `\p{Foo}`, "[b-a]", "(?m)(", "(?m))", "(?m)*",
	} {
		_, plain := regexp.Compile(expr)
		re, err := Pattern(expr)
		if (plain == nil) != (err == nil) || (err == nil) != (re != nil) {
			t.Errorf("Pattern(%q) = %v, %v; the regexp package says %v", expr, re, err, plain)
			continue
		}
		if err != nil && err.Error() != plain.Error() {
			t.Errorf("Pattern(%q) reports %q, want %q", expr, err, plain)
		}
	}
	for in, want := range map[string]string{
		"matches f (":     "matches: error parsing regexp: missing closing ): `(`",
		"matches f a)":    "matches: error parsing regexp: unexpected ): `a)`",
		"matches f (?m)(": "matches: error parsing regexp: missing closing ): `(?m)(`",
		"not matches f *": "matches: error parsing regexp: missing argument to repetition operator: `*`",
	} {
		if _, err := ParseCheck(in); err == nil || err.Error() != want {
			t.Errorf("ParseCheck(%s) error = %v, want %s", in, err, want)
		}
	}
}

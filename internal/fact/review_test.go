package fact

import (
	"strings"
	"testing"
)

const costly = "the pattern is too complicated to run quickly; make it simpler"

// A pattern is short to write and can still be enormous to run: this one is
// 23 bytes and compiles to some 1,800 steps, every one of them tried at every
// character of the file. It is turned away when the fact is stored.
func TestAPatternThatWouldBeSlowToRunIsRefused(t *testing.T) {
	for _, expr := range []string{
		`(?:[a-z ]{1,30}){1,30}Q`,
		`(?i)(?:[a-z ]{1,30}){1,30}Q`,
		`.{0,500}`,
		`[a-z]{600}[0-9]{600}`,
	} {
		if re, err := Pattern(expr); err == nil || re != nil || err.Error() != costly {
			t.Errorf("Pattern(%q) = %v, %v; want it refused as too complicated", expr, re, err)
		}
		c, err := ParseCheck("matches notes.txt '" + expr + "'")
		if err == nil || c != nil || err.Error() != "matches: "+costly {
			t.Errorf("ParseCheck with %q = %v, %v; want it refused as too complicated", expr, c, err)
		}
		// A check that did not come through ParseCheck, such as one read from
		// a journal, is held to the same limit.
		stored := &Check{Kind: "matches", Path: "notes.txt", Regex: expr}
		if err := stored.Validate(); err == nil || !strings.Contains(err.Error(), costly) {
			t.Errorf("Validate with %q = %v; want it refused as too complicated", expr, err)
		}
	}
}

func TestOrdinaryPatternsAreNotCalledSlow(t *testing.T) {
	for _, expr := range []string{
		`^go 1\.\d+$`,
		`^release:`,
		`[0-9a-f]{64}`, // a sha256
		`(?i)^the quick brown fox jumps over the lazy dog$`,
		`version\s*=\s*"\d+\.\d+\.\d+"`,
		`^(?:\d{1,3}\.){3}\d{1,3}\s+\S+$`,
		`\p{L}{100}`,
		`(?:[a-z ]{1,30}){1,15}Q`, // large, and still under the limit
		strings.Repeat("a", maxRegex),
	} {
		if _, err := Pattern(expr); err != nil {
			t.Errorf("Pattern(%q): %v", expr, err)
		}
		if _, err := ParseCheck("matches notes.txt '" + expr + "'"); err != nil {
			t.Errorf("ParseCheck with %q: %v", expr, err)
		}
	}
	// A mistake in the pattern is still reported as that mistake.
	if _, err := Pattern("(?:[a-z ]{1,30}){1,30}Q("); err == nil || strings.Contains(err.Error(), "complicated") {
		t.Errorf("a pattern that does not compile was reported as %v", err)
	}
}

package fact

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

var checkSeeds = []string{
	"file go.mod",
	"not file go.mod",
	`contains go.mod "go 1.24"`,
	`not contains "my file" 'it''s "quoted"'`,
	`matches go.mod '^go 1\.\d+$'`,
	`matches a "a\\b\"c"`,
	"matches Makefile ^release:$",
	"matches f (?-m)^a",
	"matches f (?m)(",
	"unchanged LICENSE",
	"unchanged LICENSE ABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB",
	"fresh backups/latest.tar 26h",
	"fresh a 60s",
	"fresh a 168h",
	"fresh a 2W",
	"http 200 https://staging.example.com/healthz",
	"http +200 HTTP://Example.com/?q=1#frag ok",
	`http 404 http://[::1]:8080/x "not found"`,
	"http 200 http://:80/",
	"http 200 http://user:pw@example.com/",
	"port localhost:8080",
	"port [::1]:53",
	"port localhost:+80",
	"dns example.com",
	"dns Example.COM. 93.184.216.34",
	"dns example.com.. ::1",
	"dns . ",
	"cmd systemctl is-active docker",
	`cmd echo "" 'a b' "c\"d" e\f`,
	"says active systemctl is-active docker",
	`not says "not found" ls -l`,
	"not",
	"not not file a",
	`"not" "file" a`,
	"file \xff\xfe",
	"file a\u2028b",
	"file a\u009bb",
	"file '" + strings.Repeat(`\`, 600) + "'",
	"unchanged " + strings.Repeat("a", 980),
	"",
	`"`,
	`'`,
}

// FuzzParseCheck: whatever ParseCheck accepts is valid, is written back as one
// plain line within the limit, and reads back from that line as the same check.
func FuzzParseCheck(f *testing.F) {
	for _, s := range checkSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c, err := ParseCheck(s)
		if err != nil {
			if c != nil {
				t.Fatalf("ParseCheck(%q) returned a check with its error", s)
			}
			return
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("ParseCheck(%q) accepted a check that Validate refuses: %v", s, err)
		}
		line := c.String()
		if len(line) > MaxCheck {
			t.Fatalf("ParseCheck(%q): the stored form is %d bytes", s, len(line))
		}
		if !utf8.ValidString(line) {
			t.Fatalf("ParseCheck(%q): the stored form %q is not valid UTF-8", s, line)
		}
		for _, r := range line {
			if unicode.IsControl(r) || r == 0x2028 || r == 0x2029 {
				t.Fatalf("ParseCheck(%q): the stored form %q is not one plain line", s, line)
			}
		}
		back, err := ParseCheck(line)
		if err != nil {
			t.Fatalf("ParseCheck(%q) gave %q, which does not parse: %v", s, line, err)
		}
		if !reflect.DeepEqual(c, back) {
			t.Fatalf("ParseCheck(%q) does not round-trip through %q:\n got %+v\nwant %+v", s, line, back, c)
		}
		if again := back.String(); again != line {
			t.Fatalf("String is not stable for %q: %q then %q", s, line, again)
		}
		if !c.Equal(back) || !c.Clone().Equal(c) {
			t.Fatalf("Equal is false after a round trip of %q", s)
		}
		// A pattern that was accepted compiles, with and without the line mode.
		if c.Kind == "matches" {
			if _, err := Pattern(c.Regex); err != nil {
				t.Fatalf("ParseCheck(%q) accepted a pattern that does not compile: %v", s, err)
			}
			if _, err := regexp.Compile(c.Regex); err != nil {
				t.Fatalf("ParseCheck(%q) accepted a pattern that only compiles with the line mode: %v", s, err)
			}
		}
		// Short is the same check without its digest, and parses too.
		short, err := ParseCheck(c.Short())
		if err != nil {
			t.Fatalf("Short() = %q does not parse: %v", c.Short(), err)
		}
		if short.Sum != "" && c.Kind == "unchanged" {
			t.Fatalf("Short() = %q kept the digest", c.Short())
		}
	})
}

// FuzzSplit: Split either refuses a line for an unclosed quote, or returns
// words that Quote writes back so that Split reads the same words again.
func FuzzSplit(f *testing.F) {
	for _, s := range checkSeeds {
		f.Add(s)
	}
	for _, s := range []string{`a "b c" 'd e' f`, `"a\"b\\c"`, `''""`, `a"b"'c'`, "a\tb\nc\rd", `\`, `"\`, `'\'`, "a\u00a0b", "\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		words, err := Split(s)
		if err != nil {
			if words != nil {
				t.Fatalf("Split(%q) returned words with its error", s)
			}
			if !strings.ContainsAny(s, `"'`) || !strings.Contains(err.Error(), "never closed") {
				t.Fatalf("Split(%q) failed for something other than an unclosed quote: %v", s, err)
			}
			return
		}
		if len(words) > len(s) {
			t.Fatalf("Split(%q) made %d words out of %d bytes", s, len(words), len(s))
		}
		quoted := make([]string, len(words))
		for i, w := range words {
			if !utf8.ValidString(w) {
				t.Fatalf("Split(%q) returned the invalid word %q", s, w)
			}
			quoted[i] = Quote(w)
			one, err := Split(quoted[i])
			if err != nil || len(one) != 1 || one[0] != w {
				t.Fatalf("Split(Quote(%q)) = %q, %v", w, one, err)
			}
		}
		back, err := Split(strings.Join(quoted, " "))
		if err != nil {
			t.Fatalf("Split(%q) gave %q, which does not split again: %v", s, words, err)
		}
		if len(back) != len(words) {
			t.Fatalf("Split(%q) = %q, but came back as %q", s, words, back)
		}
		for i := range words {
			if back[i] != words[i] {
				t.Fatalf("Split(%q): word %d was %q and came back as %q", s, i, words[i], back[i])
			}
		}
	})
}

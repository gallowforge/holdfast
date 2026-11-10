package fact

import (
	"reflect"
	"strings"
	"testing"
)

// awkward holds arguments that are hard to write on one line: spaces, both
// kinds of quote, backslashes, and text that looks like grammar.
var awkward = []string{
	"plain",
	"two words",
	"  padded  ",
	`say "hi"`,
	`it's`,
	`back\slash`,
	`trailing\`,
	`\"`,
	`'"\ all three`,
	"tab\there", // rejected by Validate, but Split and Quote must still carry it
	"not",
	"#hash",
	"ünïcödé ✓",
	"a\u00a0b", // a no-break space is not a separator
}

func TestSplit(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   \t\n\r ", nil},
		{"a", []string{"a"}},
		{"  a   b\tc\nd\re ", []string{"a", "b", "c", "d", "e"}},
		{`""`, []string{""}},
		{`''`, []string{""}},
		{`a "" b`, []string{"a", "", "b"}},
		{`"a b"`, []string{"a b"}},
		{`'a b'`, []string{"a b"}},
		{`"a \"b\" c"`, []string{`a "b" c`}},
		{`"a \\ b"`, []string{`a \ b`}},
		{`"a\nb"`, []string{`a\nb`}}, // only \" and \\ are escapes
		{`'a\'`, []string{`a\`}},     // nothing is an escape in single quotes
		{`'a "b" c'`, []string{`a "b" c`}},
		{`"it's"`, []string{"it's"}},
		{`a"b c"'d e'f`, []string{"ab cd ef"}}, // touching pieces are one word
		{`a\ b`, []string{`a\`, "b"}},          // a bare backslash is just a letter
		{"a\u00a0b", []string{"a\u00a0b"}},
	}
	for _, tt := range tests {
		got, err := Split(tt.in)
		if err != nil {
			t.Errorf("Split(%q): %v", tt.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Split(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSplitUnclosed(t *testing.T) {
	tests := []struct{ in, want string }{
		{`"abc`, "double quote"},
		{`a "b c`, "double quote"},
		{`"a\"`, "double quote"},
		{`"`, "double quote"},
		{`'abc`, "single quote"},
		{`a 'b c`, "single quote"},
		{`'`, "single quote"},
		{`"a'`, "double quote"},
		{`'a"`, "single quote"},
	}
	for _, tt := range tests {
		got, err := Split(tt.in)
		if err == nil {
			t.Errorf("Split(%q) = %q, want an error", tt.in, got)
			continue
		}
		if !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "never closed") {
			t.Errorf("Split(%q) error = %q, want it to name the unclosed %s", tt.in, err, tt.want)
		}
	}
}

func TestQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"", `""`},
		{"two words", `"two words"`},
		{`say "hi"`, `"say \"hi\""`},
		{"it's", `"it's"`},
		{`back\slash`, `"back\\slash"`},
		{"a\tb", "\"a\tb\""},
		{"go.mod", "go.mod"},
		{"https://example.com/a?b=c&d=e", "https://example.com/a?b=c&d=e"},
	}
	for _, tt := range tests {
		if got := Quote(tt.in); got != tt.want {
			t.Errorf("Quote(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestSplitQuoteRoundTrip(t *testing.T) {
	words := append([]string{"", " ", `"`, `'`, `\`, `\\`, `""`, "a\nb", "a\rb"}, awkward...)
	for _, w := range words {
		got, err := Split(Quote(w))
		if err != nil || len(got) != 1 || got[0] != w {
			t.Errorf("Split(Quote(%q)) = %q, %v; want the word back", w, got, err)
		}
	}
	// All of them on one line come back as the same list.
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = Quote(w)
	}
	got, err := Split(strings.Join(q, " "))
	if err != nil || !reflect.DeepEqual(got, words) {
		t.Errorf("joined line came back as %q, %v; want %q", got, err, words)
	}
}

func TestParseCheckFields(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	tests := []struct {
		in   string
		want Check
	}{
		{"file go.mod", Check{Kind: "file", Path: "go.mod"}},
		{"not file go.mod", Check{Kind: "file", Not: true, Path: "go.mod"}},
		{`contains go.mod "go 1.24"`, Check{Kind: "contains", Path: "go.mod", Text: "go 1.24"}},
		{`matches go.mod '^go 1\.\d+$'`, Check{Kind: "matches", Path: "go.mod", Regex: `^go 1\.\d+$`}},
		{"unchanged LICENSE", Check{Kind: "unchanged", Path: "LICENSE"}},
		{"unchanged LICENSE " + strings.ToUpper(sum), Check{Kind: "unchanged", Path: "LICENSE", Sum: sum}},
		{"fresh backups/latest.tar 26h", Check{Kind: "fresh", Path: "backups/latest.tar", Within: 26 * 3600}},
		{"fresh a 2W", Check{Kind: "fresh", Path: "a", Within: 14 * 24 * 3600}},
		{"http 200 https://example.com/healthz", Check{Kind: "http", Code: 200, URL: "https://example.com/healthz"}},
		{"http 200 https://example.com/ ok", Check{Kind: "http", Code: 200, URL: "https://example.com/", Text: "ok"}},
		{"port localhost:8080", Check{Kind: "port", Addr: "localhost:8080"}},
		{"port [::1]:53", Check{Kind: "port", Addr: "[::1]:53"}},
		{"dns Example.COM.", Check{Kind: "dns", Name: "example.com"}},
		{"dns example.com 93.184.216.34", Check{Kind: "dns", Name: "example.com", IP: "93.184.216.34"}},
		{"dns example.com ::1", Check{Kind: "dns", Name: "example.com", IP: "::1"}},
		{"cmd systemctl is-active docker", Check{Kind: "cmd", Argv: []string{"systemctl", "is-active", "docker"}}},
		{"cmd true", Check{Kind: "cmd", Argv: []string{"true"}}},
		{`cmd echo ""`, Check{Kind: "cmd", Argv: []string{"echo", ""}}},
		{"says active systemctl is-active docker", Check{Kind: "says", Text: "active", Argv: []string{"systemctl", "is-active", "docker"}}},
		{"not says 'not found' ls x", Check{Kind: "says", Not: true, Text: "not found", Argv: []string{"ls", "x"}}},
		{"  not   port\tlocalhost:1  ", Check{Kind: "port", Not: true, Addr: "localhost:1"}},
		{"file not", Check{Kind: "file", Path: "not"}}, // "not" only counts as the first word
	}
	for _, tt := range tests {
		got, err := ParseCheck(tt.in)
		if err != nil {
			t.Errorf("ParseCheck(%q): %v", tt.in, err)
			continue
		}
		if !reflect.DeepEqual(*got, tt.want) {
			t.Errorf("ParseCheck(%q) = %+v, want %+v", tt.in, *got, tt.want)
		}
	}
}

// everyKind builds one valid check of each kind around an awkward argument.
func everyKind(arg string) []Check {
	return []Check{
		{Kind: "file", Path: arg},
		{Kind: "contains", Path: arg, Text: arg},
		{Kind: "matches", Path: arg, Regex: "^a b'\"\\\\$"},
		{Kind: "unchanged", Path: arg},
		{Kind: "unchanged", Path: arg, Sum: strings.Repeat("0f", 32)},
		{Kind: "fresh", Path: arg, Within: 90},
		{Kind: "fresh", Path: arg, Within: 7 * 24 * 3600},
		{Kind: "http", Code: 200, URL: "https://example.com/a?b=c"},
		{Kind: "http", Code: 503, URL: "http://127.0.0.1:8080/", Text: arg},
		{Kind: "port", Addr: "localhost:8080"},
		{Kind: "dns", Name: "example.com"},
		{Kind: "dns", Name: "example.com", IP: "2001:db8::1"},
		{Kind: "cmd", Argv: []string{"prog", arg, "", arg}},
		{Kind: "says", Text: arg, Argv: []string{"prog", arg}},
	}
}

func TestParseCheckRoundTrip(t *testing.T) {
	for _, arg := range awkward {
		if strings.ContainsAny(arg, "\t") {
			continue // control characters are not valid in a check
		}
		for _, c := range everyKind(arg) {
			for _, not := range []bool{false, true} {
				c := c
				c.Not = not
				if err := c.Validate(); err != nil {
					t.Fatalf("test check %+v is not valid: %v", c, err)
				}
				line := c.String()
				got, err := ParseCheck(line)
				if err != nil {
					t.Errorf("ParseCheck(%s): %v", line, err)
					continue
				}
				if !reflect.DeepEqual(*got, c) {
					t.Errorf("round trip of %s\n got %+v\nwant %+v", line, *got, c)
				}
				if again := got.String(); again != line {
					t.Errorf("String is not stable: %s then %s", line, again)
				}
				if !got.Equal(&c) {
					t.Errorf("Equal is false after a round trip of %s", line)
				}
				if strings.HasPrefix(line, "not ") != not {
					t.Errorf("String() = %s, not = %v", line, not)
				}
			}
		}
	}
}

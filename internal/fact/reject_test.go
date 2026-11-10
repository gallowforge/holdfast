package fact

import (
	"strings"
	"testing"
)

func TestParseCheckRejects(t *testing.T) {
	long := strings.Repeat("a", maxRegex+1)
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", "the check is empty"},
		{"only spaces", "   ", "the check is empty"},
		{"only not", "not", "the check is empty"},
		{"unknown kind", "exists go.mod", `unknown check "exists"`},
		{"double not", "not not file a", `unknown check "not"`},
		{"kind is case sensitive", "File go.mod", `unknown check "File"`},
		{"unclosed double quote", `contains a "b`, "double quote is never closed"},
		{"unclosed single quote", `contains a 'b`, "single quote is never closed"},
		{"too long", "file " + strings.Repeat("a", MaxCheck), "the limit is 1000"},

		{"file no args", "file", "file takes 1 argument"},
		{"file two args", "file a b", "file takes 1 argument"},
		{"file empty path", `file ""`, "the path is empty"},
		{"contains one arg", "contains a", "contains takes 2 arguments"},
		{"contains three args", "contains a b c", "contains takes 2 arguments"},
		{"contains empty text", `contains a ""`, "the text to look for is empty"},
		{"contains empty path", `contains "" a`, "the path is empty"},
		{"matches one arg", "matches a", "matches takes 2 arguments"},
		{"matches empty regex", `matches a ""`, "the regular expression is empty"},
		{"matches bad regex", "matches a (", "matches: error parsing regexp"},
		{"matches unsupported regex", `matches a "(?=a)"`, "matches: error parsing regexp"},
		{"matches long regex", "matches a " + long, "longer than 512 bytes"},
		{"unchanged no args", "unchanged", "unchanged takes 1 or 2 arguments"},
		{"unchanged three args", "unchanged a b c", "unchanged takes 1 or 2 arguments"},
		{"unchanged short digest", "unchanged a abc123", "not a sha256"},
		{"unchanged non-hex digest", "unchanged a " + strings.Repeat("g", 64), "not a sha256"},
		{"unchanged long digest", "unchanged a " + strings.Repeat("a", 65), "not a sha256"},
		{"fresh one arg", "fresh a", "fresh takes 2 arguments"},
		{"fresh bad unit", "fresh a 5y", "the unit must be s, m, h, d or w"},
		{"fresh no number", "fresh a h", "write a number and a unit"},
		{"fresh zero", "fresh a 0h", "must be more than zero"},
		{"fresh negative", "fresh a -5h", "write a whole number"},
		{"fresh too long", "fresh a 9999w", "too long"},

		{"http one arg", "http 200", "http takes 2 or 3 arguments"},
		{"http four args", "http 200 http://a b c", "http takes 2 or 3 arguments"},
		{"http status not a number", "http ok http://a", `"ok" is not a status code`},
		{"http status and url swapped", "http http://a 200", "is not a status code"},
		{"http status 99", "http 99 http://a", "99 is not a status code"},
		{"http status 600", "http 600 http://a", "600 is not a status code"},
		{"http status 0", "http 0 http://a", "0 is not a status code"},
		{"http status negative", "http -200 http://a", "-200 is not a status code"},
		{"http ftp", "http 200 ftp://example.com/", "is not an http or https address"},
		{"http file scheme", "http 200 file:///etc/passwd", "is not an http or https address"},
		{"http no scheme", "http 200 example.com/health", "is not an http or https address"},
		{"http no host", "http 200 http:///path", "is not an http or https address"},
		{"http opaque", "http 200 http:example.com", "is not an http or https address"},
		{"http port but no host", "http 200 http://:8080/", "is not an http or https address"},
		{"http space in host", `http 200 "http://exa mple.com/"`, "is not an http or https address"},
		{"http user and password", "http 200 https://user:pw@example.com/", "never put credentials"},
		{"http user only", "http 200 https://user@example.com/", "never put credentials"},
		{"http empty userinfo", "http 200 https://@example.com/", "never put credentials"},

		{"port no args", "port", "port takes 1 argument"},
		{"port no port", "port localhost", "is not host:port"},
		{"port no host", "port :8080", "is not host:port"},
		{"port not a number", "port localhost:http", "is not a port number"},
		{"port zero", "port localhost:0", "is not a port number"},
		{"port too big", "port localhost:65536", "is not a port number"},
		{"port negative", "port localhost:-1", "is not a port number"},
		{"port bare ipv6", "port ::1:80", "is not host:port"},
		{"port is a url", "port http://localhost:80", "is not host:port"},

		{"dns no args", "dns", "dns takes 1 or 2 arguments"},
		{"dns three args", "dns a b c", "dns takes 1 or 2 arguments"},
		{"dns only a dot", "dns .", "is not a host name"},
		{"dns space", `dns "a b"`, "is not a host name"},
		{"dns url", "dns https://example.com", "is not a host name"},
		{"dns path", "dns example.com/x", "is not a host name"},
		{"dns with port", "dns example.com:53", "is not a host name"},
		{"dns userinfo", "dns user@example.com", "is not a host name"},
		{"dns two trailing dots", "dns example.com..", "is not a host name"},
		{"dns bad ip", "dns example.com 1.2.3", "is not an IP address"},
		{"dns ip is a name", "dns example.com other.com", "is not an IP address"},

		{"cmd no program", "cmd", "cmd takes a program"},
		{"cmd empty program", `cmd ""`, "no program given"},
		{"cmd empty program with args", `cmd "" a`, "no program given"},
		{"says no args", "says", "says takes the expected text"},
		{"says no program", "says active", "says takes the expected text"},
		{"says empty text", `says "" systemctl`, "the expected text is empty"},
		{"says empty program", `says a ""`, "no program given"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, prefix := range []string{"", "not "} {
				if tt.in == "not" && prefix != "" || tt.name == "double not" && prefix != "" {
					continue
				}
				c, err := ParseCheck(prefix + tt.in)
				if err == nil {
					t.Fatalf("ParseCheck(%q) = %+v, want an error", prefix+tt.in, *c)
				}
				if !strings.Contains(err.Error(), tt.want) {
					t.Errorf("ParseCheck(%q) error = %q, want it to contain %q", prefix+tt.in, err, tt.want)
				}
			}
		})
	}
}

// An unknown or empty check tells the reader what the kinds are.
func TestParseCheckErrorListsKinds(t *testing.T) {
	for _, in := range []string{"", "nonsense x"} {
		_, err := ParseCheck(in)
		if err == nil {
			t.Fatalf("ParseCheck(%q) did not fail", in)
		}
		for _, k := range Kinds {
			if !strings.Contains(err.Error(), k) {
				t.Errorf("ParseCheck(%q) error %q does not mention %s", in, err, k)
			}
		}
	}
}

// Control characters are refused in every argument of every kind, because a
// check is shown in terminals, journals and Markdown lists as one line.
func TestControlCharactersRejected(t *testing.T) {
	controls := map[string]string{
		"NUL": "\x00", "bell": "\a", "tab": "\t", "newline": "\n", "carriage return": "\r",
		"escape": "\x1b", "unit separator": "\x1f", "delete": "\x7f",
		"C1 CSI": "\u009b", "C1 NEL": "\u0085", "line separator": "\u2028", "paragraph separator": "\u2029",
	}
	fields := map[string]func(bad string) Check{
		"path":        func(b string) Check { return Check{Kind: "file", Path: "a" + b + "b"} },
		"text":        func(b string) Check { return Check{Kind: "contains", Path: "a", Text: "a" + b + "b"} },
		"regex":       func(b string) Check { return Check{Kind: "matches", Path: "a", Regex: "a" + b + "b"} },
		"url":         func(b string) Check { return Check{Kind: "http", Code: 200, URL: "http://example.com/" + b} },
		"http text":   func(b string) Check { return Check{Kind: "http", Code: 200, URL: "http://example.com/", Text: b} },
		"addr":        func(b string) Check { return Check{Kind: "port", Addr: "local" + b + "host:80"} },
		"name":        func(b string) Check { return Check{Kind: "dns", Name: "exa" + b + "mple.com"} },
		"ip":          func(b string) Check { return Check{Kind: "dns", Name: "example.com", IP: "::1" + b} },
		"program":     func(b string) Check { return Check{Kind: "cmd", Argv: []string{"ls" + b}} },
		"argument":    func(b string) Check { return Check{Kind: "cmd", Argv: []string{"ls", "-l", b}} },
		"says text":   func(b string) Check { return Check{Kind: "says", Text: b, Argv: []string{"ls"}} },
		"fresh path":  func(b string) Check { return Check{Kind: "fresh", Path: b, Within: 60} },
		"sealed path": func(b string) Check { return Check{Kind: "unchanged", Path: b, Sum: strings.Repeat("a", 64)} },
	}
	for cname, bad := range controls {
		for fname, build := range fields {
			c := build(bad)
			err := c.Validate()
			if err == nil {
				t.Errorf("%s in the %s: Validate accepted %q", cname, fname, c.String())
				continue
			}
			if strings.ContainsAny(err.Error(), "\x00\a\t\n\r\x1b\x1f\x7f\u009b\u0085\u2028\u2029") {
				t.Errorf("%s in the %s: the error itself carries the control character: %q", cname, fname, err)
			}
			// The same through the parser, where the character can be quoted in.
			if _, err := ParseCheck(c.String()); err == nil {
				t.Errorf("%s in the %s: ParseCheck accepted %q", cname, fname, c.String())
			}
		}
	}
}

func TestValidateRejectsWhatParseCannotProduce(t *testing.T) {
	tests := []struct {
		name string
		c    Check
		want string
	}{
		{"no kind", Check{}, "unknown check"},
		{"unknown kind", Check{Kind: "exec", Argv: []string{"ls"}}, "unknown check"},
		{"fresh without a duration", Check{Kind: "fresh", Path: "a"}, "must be more than zero"},
		{"fresh negative", Check{Kind: "fresh", Path: "a", Within: -5}, "must be more than zero"},
		{"uppercase digest", Check{Kind: "unchanged", Path: "a", Sum: strings.Repeat("A", 64)}, "not a sha256"},
		{"cmd nil argv", Check{Kind: "cmd"}, "no program given"},
		{"says nil argv", Check{Kind: "says", Text: "a"}, "no program given"},
		{"http code zero", Check{Kind: "http", URL: "http://example.com"}, "0 is not a status code"},
		{"port empty", Check{Kind: "port"}, "is not host:port"},
		{"dns empty", Check{Kind: "dns"}, "is not a host name"},
		{"dns trailing dot", Check{Kind: "dns", Name: "example.com."}, "is not a host name"},
	}
	for _, tt := range tests {
		err := tt.c.Validate()
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Validate() = %v, want an error containing %q", tt.name, err, tt.want)
		}
	}
}

func TestShortAndEqual(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	sealed := &Check{Kind: "unchanged", Path: "a b", Sum: sum}
	if got, want := sealed.String(), `unchanged "a b" `+sum; got != want {
		t.Errorf("String() = %s, want %s", got, want)
	}
	if got, want := sealed.Short(), `unchanged "a b"`; got != want {
		t.Errorf("Short() = %s, want %s", got, want)
	}
	if sealed.Sum != sum {
		t.Error("Short() changed the check it was called on")
	}
	plain := &Check{Kind: "file", Path: "a"}
	if plain.Short() != plain.String() {
		t.Errorf("Short() = %s, want %s", plain.Short(), plain.String())
	}

	var none *Check
	if !none.Equal(nil) {
		t.Error("nil.Equal(nil) = false")
	}
	if none.Equal(plain) || plain.Equal(nil) {
		t.Error("a check equals no check")
	}
	if !plain.Equal(&Check{Kind: "file", Path: "a"}) {
		t.Error("two identical checks are not equal")
	}
	if plain.Equal(&Check{Kind: "file", Path: "a", Not: true}) {
		t.Error("a check equals its negation")
	}
	if sealed.Equal(&Check{Kind: "unchanged", Path: "a b"}) {
		t.Error("a sealed check equals the unsealed one")
	}
}

// The limit applies to the check as it is stored, not as it was typed: quoting
// can make the stored line longer, and "unchanged" gains a digest. A check
// that would not read back from the journal is refused when it is written.
func TestCheckLengthLimit(t *testing.T) {
	sum := strings.Repeat("0", 64)
	accepted := []string{
		"file " + strings.Repeat("a", MaxCheck-5),
		"file '" + strings.Repeat("a b", (MaxCheck-7)/3) + "'",
		"unchanged " + strings.Repeat("a", MaxCheck-10-65),
		"unchanged " + strings.Repeat("a", MaxCheck-10-65) + " " + sum,
	}
	for _, in := range accepted {
		c, err := ParseCheck(in)
		if err != nil {
			t.Errorf("a %d-byte check was refused: %v", len(in), err)
			continue
		}
		if c.Kind == "unchanged" {
			c.Sum = sum // what Seal does when the fact is stored
		}
		if n := len(c.String()); n > MaxCheck {
			t.Errorf("a %d-byte check is stored as %d bytes", len(in), n)
		}
		if _, err := ParseCheck(c.String()); err != nil {
			t.Errorf("a %d-byte check was accepted, but its stored form is not: %v", len(in), err)
		}
	}
	refused := []string{
		"file " + strings.Repeat("a", MaxCheck-4),            // typed too long
		"file '" + strings.Repeat(`\`, MaxCheck-7) + "'",     // every backslash doubles
		"cmd " + strings.Repeat(`'"' `, (MaxCheck-4)/4),      // every quote gains a backslash
		"file " + strings.Repeat("\xff", MaxCheck-5),         // every bad byte becomes three
		"unchanged " + strings.Repeat("a", MaxCheck-10),      // no room left for the digest
		"unchanged " + strings.Repeat("a", MaxCheck-10-65+1), // one byte short of room
	}
	for _, in := range refused {
		if c, err := ParseCheck(in); err == nil {
			t.Errorf("a %d-byte check stored as %d bytes was accepted", len(in), len(c.String()))
		} else if !strings.Contains(err.Error(), "the limit is 1000") {
			t.Errorf("a check that is too long was refused with %q", err)
		}
	}
}

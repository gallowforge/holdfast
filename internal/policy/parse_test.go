package policy

import (
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

// dump shows a policy field by field (printing it with %v would write the
// whole file, header and all).
func dump(p *Policy) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("files=%q net=%v cmds=%q", p.Files, p.Net, p.Cmds)
}

func TestDefault(t *testing.T) {
	p := Default()
	want := &Policy{Files: []string{"."}, Net: []NetRule{{Class: "loopback"}}}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("Default() = %s, want the project folder and loopback only", dump(p))
	}
	if len(p.Cmds) != 0 {
		t.Error("the default policy allows a command")
	}
}

func TestParse(t *testing.T) {
	text := `# a comment
   # an indented comment

files . docs "my folder" ~/notes
files /srv/app
	net loopback Private 10.0.0.0/8 192.168.1.5
net example.com *.Example.org. ::1 fe80::/10 LOCALHOST
cmd git status
cmd systemctl is-active *
cmd make "-C" 'sub dir' **
`
	p, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	want := &Policy{
		Files: []string{".", "docs", "my folder", "~/notes", "/srv/app"},
		Net: []NetRule{
			{Class: "loopback"}, {Class: "private"}, {Prefix: pfx("10.0.0.0/8")}, {Prefix: pfx("192.168.1.5/32")},
			{Name: "example.com"}, {Name: "*.example.org"}, {Prefix: pfx("::1/128")}, {Prefix: pfx("fe80::/10")}, {Class: "loopback"},
		},
		Cmds: [][]string{{"git", "status"}, {"systemctl", "is-active", "*"}, {"make", "-C", "sub dir", "**"}},
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("Parse:\n got %s\nwant %s", dump(p), dump(want))
	}
}

func TestParseEmpty(t *testing.T) {
	for _, text := range []string{"", "\n\n", "# only a comment", "  \t \n#x\n\r\n", Header} {
		p, err := Parse(text)
		if err != nil {
			t.Errorf("Parse(%q): %v", text, err)
			continue
		}
		if len(p.Files)+len(p.Net)+len(p.Cmds) != 0 {
			t.Errorf("Parse(%q) allows something: %s", text, dump(p))
		}
	}
}

// A "#" only starts a comment at the start of a line.
func TestParseHashInsideALine(t *testing.T) {
	p, err := Parse("files a #b\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "#b"}; !reflect.DeepEqual(p.Files, want) {
		t.Errorf("Files = %q, want %q", p.Files, want)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct{ name, text, want string }{
		{"unknown word", "files .\nallow everything\n", `policy line 2: unknown word "allow" (expected files, net or cmd)`},
		{"line number counts comments and blanks", "# one\n\nfiles .\n\n# five\nnets loopback\n", "policy line 6: unknown word"},
		{"directives are lowercase", "Files .", `policy line 1: unknown word "Files"`},
		{"files with nothing", "files\n", `policy line 1: "files" allows nothing`},
		{"net with nothing", "files .\nnet   \n", `policy line 2: "net" allows nothing`},
		{"cmd with nothing", "\n\ncmd", `policy line 3: "cmd" allows nothing`},
		{"unknown word with nothing", "bogus", `policy line 1: "bogus" allows nothing`},
		{"unclosed quote", "files .\nfiles \"my folder\n", "policy line 2: a double quote is never closed"},
		{"unclosed single quote", "cmd echo 'a", "policy line 1: a single quote is never closed"},
		{"bad net rule", "net loopback\nnet host:80", `policy line 2: net rule "host:80"`},
		{"second rule on a line is bad", "net loopback user@host", `policy line 1: net rule "user@host"`},
		{"cmd star program", "cmd * status", "policy line 1: a cmd rule must name its program"},
		{"cmd double star program", "cmd **", "policy line 1: a cmd rule must name its program"},
		{"cmd double star in the middle", "cmd git ** status", "policy line 1: ** may only end a cmd rule"},
		{"cmd double star twice", "cmd git ** **", "policy line 1: ** may only end a cmd rule"},
	}
	for _, tt := range tests {
		p, err := Parse(tt.text)
		if err == nil {
			t.Errorf("%s: Parse succeeded: %s", tt.name, dump(p))
			continue
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error = %q, want it to contain %q", tt.name, err, tt.want)
		}
		if p != nil {
			t.Errorf("%s: Parse returned a policy with its error; a half-read policy must not be used", tt.name)
		}
	}
}

func TestParseNetRule(t *testing.T) {
	good := []struct {
		in   string
		want NetRule
		str  string
	}{
		{"loopback", NetRule{Class: "loopback"}, "loopback"},
		{"LOOPBACK", NetRule{Class: "loopback"}, "loopback"},
		{"localhost", NetRule{Class: "loopback"}, "loopback"},
		{"Localhost.", NetRule{Class: "loopback"}, "loopback"},
		{"private", NetRule{Class: "private"}, "private"},
		{"Public", NetRule{Class: "public"}, "public"},
		{"10.0.0.0/8", NetRule{Prefix: pfx("10.0.0.0/8")}, "10.0.0.0/8"},
		{"10.1.2.3/8", NetRule{Prefix: pfx("10.0.0.0/8")}, "10.0.0.0/8"}, // host bits are dropped
		{"192.168.1.5", NetRule{Prefix: pfx("192.168.1.5/32")}, "192.168.1.5"},
		{"192.168.1.5/32", NetRule{Prefix: pfx("192.168.1.5/32")}, "192.168.1.5"},
		{"::1", NetRule{Prefix: pfx("::1/128")}, "::1"},
		{"FD00::/8", NetRule{Prefix: pfx("fd00::/8")}, "fd00::/8"},
		{"::ffff:10.0.0.5", NetRule{Prefix: pfx("10.0.0.5/32")}, "10.0.0.5"}, // a mapped address is the IPv4 one
		{"169.254.169.254", NetRule{Prefix: pfx("169.254.169.254/32")}, "169.254.169.254"},
		{"0.0.0.0/0", NetRule{Prefix: pfx("0.0.0.0/0")}, "0.0.0.0/0"},
		{"example.com", NetRule{Name: "example.com"}, "example.com"},
		{"Example.COM.", NetRule{Name: "example.com"}, "example.com"},
		{"*.example.com", NetRule{Name: "*.example.com"}, "*.example.com"},
		{"*.COM", NetRule{Name: "*.com"}, "*.com"},
		{"db", NetRule{Name: "db"}, "db"},
	}
	for _, tt := range good {
		got, err := ParseNetRule(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParseNetRule(%q) = %+v, %v; want %+v", tt.in, got, err, tt.want)
			continue
		}
		if got.String() != tt.str {
			t.Errorf("ParseNetRule(%q).String() = %q, want %q", tt.in, got.String(), tt.str)
		}
	}
	for _, in := range []string{
		"", ".", "*", "*.", "**", "*.*", "*.*.com", "a.*.com", "*example.com", "ex*mple.com",
		".com", "*..com", "a b", "a\tb", "private\tpublic", "a\nb", "a\x00b", `a"b`, "it's", `a\b`, "user@host", "host:80", "http://example.com", "example.com/path",
		"10.0.0.0/33", "10.0.0.0/-1", "[::1]", "[::1]:80",
	} {
		if got, err := ParseNetRule(in); err == nil {
			t.Errorf("ParseNetRule(%q) = %+v, want an error", in, got)
		}
	}
}

func TestStringRoundTrip(t *testing.T) {
	policies := map[string]*Policy{
		"empty":   {},
		"default": Default(),
		"full": {
			Files: []string{".", "docs", "my folder", `we"ird`, "it's", `back\slash`, "~/notes", "/srv/app", "#not-a-comment"},
			Net: []NetRule{
				{Class: "loopback"}, {Class: "private"}, {Class: "public"},
				{Prefix: pfx("10.0.0.0/8")}, {Prefix: pfx("192.168.1.5/32")}, {Prefix: pfx("::1/128")}, {Prefix: pfx("fd00::/8")},
				{Name: "example.com"}, {Name: "*.example.org"},
			},
			Cmds: [][]string{{"git", "status"}, {"systemctl", "is-active", "*"}, {"make", "-C", "sub dir", "**"}, {"echo", "", `say "hi"`}, {"./scripts/check.sh"}},
		},
		"only commands": {Cmds: [][]string{{"true"}}},
	}
	for name, p := range policies {
		text := p.String()
		if !strings.HasPrefix(text, Header) {
			t.Errorf("%s: String() does not start with the header", name)
		}
		back, err := Parse(text)
		if err != nil {
			t.Errorf("%s: Parse(String()): %v\n%s", name, err, text)
			continue
		}
		if !reflect.DeepEqual(back, p) {
			t.Errorf("%s: round trip\n got %s\nwant %s", name, dump(back), dump(p))
		}
		if again := back.String(); again != text {
			t.Errorf("%s: String() is not stable:\n%s\n---\n%s", name, text, again)
		}
	}
}

// Whatever Parse accepts, String writes so that Parse reads the same policy
// again: nothing may turn into a second, wider rule on the way through a file.
func TestParseStringParse(t *testing.T) {
	texts := []string{
		"files 'a b' \"c\\\"d\" e\\f\n",
		"net 'odd\"name' \"tab\tname\" 'spaced name'\n",
		"net \"private\tpublic\"\n",
		"net 'back\\slash' \"it's\"\n",
		"cmd echo 'a b' \"\" '\"'\n",
		"files a\nfiles b\nnet loopback\nnet 10.0.0.1\ncmd a\ncmd b **\n",
	}
	for _, text := range texts {
		p, err := Parse(text)
		if err != nil {
			continue // refusing an odd line is fine; reading it back wrongly is not
		}
		back, err := Parse(p.String())
		if err != nil {
			t.Errorf("Parse(%q) was accepted, but what String wrote is not: %v", text, err)
			continue
		}
		if !reflect.DeepEqual(back, p) {
			t.Errorf("Parse(%q) changed on the way through String:\n got %s\nwant %s", text, dump(back), dump(p))
		}
	}
}

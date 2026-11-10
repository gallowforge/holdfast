package policy

import (
	"strings"
	"testing"
)

type hostCase struct {
	host string
	ok   bool
}

type connCase struct {
	host, addr string
	ok         bool
}

func checkRules(t *testing.T, text string, hosts []hostCase, conns []connCase) {
	t.Helper()
	p := mustParse(t, text)
	for _, c := range hosts {
		if got := verdict(t, p.Host(c.host)); (got == allowed) != c.ok {
			t.Errorf("policy %q: Host(%q) allowed = %v, want %v (%s)", text, c.host, got == allowed, c.ok, got)
		}
	}
	for _, c := range conns {
		if got := verdict(t, p.Conn(c.host, ip(c.addr))); (got == allowed) != c.ok {
			t.Errorf("policy %q: Conn(%q, %s) allowed = %v, want %v (%s)", text, c.host, c.addr, got == allowed, c.ok, got)
		}
	}
}

// A host a person named is trusted wherever it resolves, except to a
// link-local address, which only an address or range rule allows.
func TestNamedHost(t *testing.T) {
	checkRules(t, "net example.com",
		[]hostCase{
			{"example.com", true}, {"EXAMPLE.com.", true},
			{"www.example.com", false}, {"notexample.com", false}, {"example.com.evil.org", false},
			{"example.org", false}, {"localhost", false}, {"127.0.0.1", false}, {"93.184.216.34", false},
		},
		[]connCase{
			{"example.com", "93.184.216.34", true},
			{"example.com", "2606:2800:220:1::1", true},
			{"Example.COM.", "93.184.216.34", true},
			{"example.com", "10.0.0.5", true},
			{"example.com", "127.0.0.1", true},
			{"example.com", "::ffff:10.0.0.5", true},
			{"example.com", "169.254.169.254", false},
			{"example.com", "::ffff:169.254.169.254", false},
			{"example.com", "fe80::1", false},
			{"example.com", "224.0.0.1", false},
			{"example.com", "255.255.255.255", false},
			{"www.example.com", "93.184.216.34", false},
			{"other.org", "93.184.216.34", false},
			// The rule is for the name, not for wherever the name happens to live.
			{"93.184.216.34", "93.184.216.34", false},
		})
	p := mustParse(t, "net example.com")
	want := "example.com is a link-local address (169.254.169.254), which is only allowed by its exact address. A person can allow it: holdfast allow net 169.254.169.254"
	if got := verdict(t, p.Conn("example.com", ip("169.254.169.254"))); got != want {
		t.Errorf("a named host on a link-local address:\n got %q\nwant %q", got, want)
	}
}

func TestLinkLocalNeedsItsAddress(t *testing.T) {
	meta := []connCase{{"example.com", "169.254.169.254", true}, {"169.254.169.254", "169.254.169.254", true}, {"example.com", "::ffff:169.254.169.254", true}}
	for _, rule := range []string{"169.254.169.254", "169.254.0.0/16", "0.0.0.0/0", "::ffff:169.254.169.254"} {
		checkRules(t, "net example.com "+rule, []hostCase{{"169.254.169.254", true}}, meta)
	}
	checkRules(t, "net example.com fe80::/10", nil, []connCase{{"example.com", "fe80::1", true}, {"example.com", "169.254.169.254", false}})
	// No class takes it in, not even all three together.
	checkRules(t, "net loopback private public example.com *.example.com",
		[]hostCase{{"169.254.169.254", false}, {"fe80::1", false}},
		[]connCase{{"example.com", "169.254.169.254", false}, {"a.example.com", "fe80::1", false}, {"anything.test", "169.254.0.1", false}})
	// A different address in the range is still refused.
	checkRules(t, "net 169.254.169.254", nil, []connCase{{"x.test", "169.254.169.253", false}})
}

// "*.example.com" is every name under example.com, at any depth, and not
// example.com itself: that needs its own rule.
func TestWildcardHost(t *testing.T) {
	checkRules(t, "net *.example.com",
		[]hostCase{
			{"a.example.com", true}, {"A.Example.Com.", true}, {"a.b.example.com", true},
			{"example.com", false}, {".example.com", false}, {"badexample.com", false},
			{"a.example.com.evil.org", false}, {"example.com.evil.org", false}, {"a.example.org", false},
			{"*.example.com", true}, // as a host name it is merely odd, and under example.com
		},
		[]connCase{
			{"a.example.com", "93.184.216.34", true},
			{"a.b.example.com", "10.0.0.5", true},
			{"a.example.com", "169.254.169.254", false},
			{"example.com", "93.184.216.34", false},
			{"badexample.com", "93.184.216.34", false},
		})
	checkRules(t, "net *.example.com example.com", []hostCase{{"example.com", true}, {"a.example.com", true}}, nil)
	for _, tt := range []struct {
		rule, host string
		want       bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "a.example.com", false},
		{"*.example.com", "example.com", false},
		{"*.example.com", "x.example.com", true},
		{"*.com", "example.com", true},
		{"*.com", "com", false},
		{"", "", false},
		{"", "example.com", false},
	} {
		if got := matchName(tt.rule, tt.host); got != tt.want {
			t.Errorf("matchName(%q, %q) = %v, want %v", tt.rule, tt.host, got, tt.want)
		}
	}
}

func TestClassRules(t *testing.T) {
	private := []connCase{
		{"db.internal", "10.0.0.5", true}, {"db.internal", "172.16.0.1", true}, {"db.internal", "172.31.255.255", true},
		{"db.internal", "192.168.1.1", true}, {"db.internal", "100.64.0.1", true}, {"db.internal", "fd00::1", true},
		{"10.0.0.5", "10.0.0.5", true}, {"db.internal", "::ffff:192.168.1.1", true},
		{"db.internal", "172.32.0.1", false}, {"db.internal", "8.8.8.8", false}, {"db.internal", "2001:db8::1", false},
		{"db.internal", "127.0.0.1", false}, {"db.internal", "::1", false},
		{"db.internal", "169.254.169.254", false}, {"db.internal", "fe80::1", false}, {"db.internal", "224.0.0.1", false},
	}
	// Opening a whole network lets names be looked up; the address then decides.
	checkRules(t, "net private", []hostCase{{"db.internal", true}, {"example.com", true}, {"10.0.0.5", true}, {"8.8.8.8", false}, {"localhost", false}, {"127.0.0.1", false}}, private)

	public := []connCase{
		{"example.com", "8.8.8.8", true}, {"example.com", "2606:4700:4700::1111", true}, {"8.8.8.8", "8.8.8.8", true},
		{"example.com", "10.0.0.5", false}, {"example.com", "192.168.1.1", false}, {"example.com", "100.64.0.1", false},
		{"example.com", "fd00::1", false}, {"example.com", "127.0.0.1", false}, {"example.com", "::ffff:127.0.0.1", false},
		{"example.com", "169.254.169.254", false}, {"example.com", "::ffff:169.254.169.254", false},
		{"example.com", "0.0.0.0", false}, {"example.com", "255.255.255.255", false}, {"example.com", "ff02::1", false},
	}
	checkRules(t, "net public", []hostCase{{"example.com", true}, {"8.8.8.8", true}, {"10.0.0.5", false}, {"localhost", false}, {"[::1]", false}}, public)

	checkRules(t, "net loopback private public",
		[]hostCase{{"localhost", true}, {"example.com", true}, {"10.0.0.1", true}, {"8.8.8.8", true}, {"169.254.169.254", false}, {"224.0.0.1", false}},
		[]connCase{{"a.test", "127.0.0.1", true}, {"a.test", "10.0.0.1", true}, {"a.test", "8.8.8.8", true}, {"a.test", "169.254.169.254", false}, {"a.test", "239.1.2.3", false}})
}

// A range allows addresses, not look-ups: a name is only resolved when a
// person named it or opened the private or public network as a whole.
func TestRangeRules(t *testing.T) {
	checkRules(t, "net 10.0.0.0/8 2001:db8::/32",
		[]hostCase{{"10.1.2.3", true}, {"[2001:db8::5]", true}, {"::ffff:10.1.2.3", true}, {"11.0.0.1", false}, {"db.internal", false}, {"localhost", false}},
		[]connCase{
			{"10.1.2.3", "10.1.2.3", true}, {"db.internal", "10.1.2.3", true}, {"db.internal", "::ffff:10.1.2.3", true},
			{"db.internal", "2001:db8::5", true}, {"db.internal", "11.0.0.1", false}, {"db.internal", "2001:db9::5", false},
		})
	// "localhost" may be looked up when loopback is allowed by class or by address.
	checkRules(t, "net 127.0.0.1", []hostCase{{"localhost", true}, {"x.localhost", true}, {"127.0.0.1", true}, {"127.0.0.2", false}, {"::1", false}},
		[]connCase{{"localhost", "127.0.0.1", true}, {"localhost", "::1", false}, {"localhost", "127.0.0.2", false}})
	checkRules(t, "net ::1", []hostCase{{"localhost", true}, {"[::1]", true}, {"127.0.0.1", false}}, []connCase{{"localhost", "::1", true}, {"localhost", "127.0.0.1", false}})
	// A range written in the IPv4-in-IPv6 form matches nothing (it fails closed).
	checkRules(t, "net ::ffff:10.0.0.0/104", []hostCase{{"10.0.0.5", false}, {"::ffff:10.0.0.5", false}}, []connCase{{"a.test", "10.0.0.5", false}})
}

// Multicast and broadcast are never dialled, whatever the policy says.
func TestNeverAllowed(t *testing.T) {
	p := mustParse(t, "net loopback private public 0.0.0.0/0 ::/0 224.0.0.0/4 ff00::/8 255.255.255.255 224.0.0.1 ff02::1 example.com *.example.com")
	for _, a := range []string{"224.0.0.1", "239.255.255.250", "255.255.255.255", "ff02::1", "ff05::2", "::ffff:224.0.0.1", "::ffff:255.255.255.255"} {
		for _, h := range []string{a, "example.com", "a.example.com", "localhost"} {
			got := verdict(t, p.Conn(h, ip(a)))
			if !strings.HasPrefix(got, "Holdfast never connects to ") {
				t.Errorf("Conn(%q, %s) = %q, want a flat refusal", h, a, got)
			}
			if strings.Contains(got, "A person can allow it") {
				t.Errorf("Conn(%q, %s) = %q offers a way to allow it, and there is none", h, a, got)
			}
		}
		if p.Host(a) == nil {
			t.Errorf("Host(%s) is allowed", a)
		}
	}
	// The same wide-open policy does allow everything else, link-local included.
	for _, a := range []string{"127.0.0.1", "10.0.0.1", "8.8.8.8", "169.254.169.254", "fe80::1", "2001:db8::1"} {
		if err := p.Conn("x.test", ip(a)); err != nil {
			t.Errorf("Conn(x.test, %s) = %v under a wide-open policy", a, err)
		}
	}
}

func TestMatchArgv(t *testing.T) {
	tests := []struct {
		pat, argv string // space separated; "-" for an empty list
		want      bool
	}{
		{"git status", "git status", true},
		{"git status", "git", false},
		{"git status", "git status -s", false},
		{"git status", "git log", false},
		{"git", "git", true},
		{"git", "git status", false},

		// The program is matched exactly.
		{"git", "/usr/bin/git", false},
		{"git", "./git", false},
		{"git", "bin/git", false},
		{"git", "GIT", false},
		{"git", "git ", false},
		{"/usr/bin/git", "git", false},
		{"./git", "git", false},
		{"/usr/bin/git status", "/usr/bin/git status", true},

		// * is exactly one argument.
		{"systemctl is-active *", "systemctl is-active docker", true},
		{"systemctl is-active *", "systemctl is-active", false},
		{"systemctl is-active *", "systemctl is-active docker nginx", false},
		{"systemctl is-active *", "systemctl restart docker", false},
		{"cp * *", "cp a b", true},
		{"cp * *", "cp a", false},
		{"cp * *", "cp a b c", false},
		{"git * --stat", "git log --stat", true},
		{"git * --stat", "git log --oneline", false},
		{"git *", "git *", true},
		{"git *", "git **", true},

		// ** at the end is zero or more arguments.
		{"make **", "make", true},
		{"make **", "make test", true},
		{"make **", "make -C sub test", true},
		{"make **", "cmake", false},
		{"make **", "cmake test", false},
		{"git log **", "git log", true},
		{"git log **", "git log -n 5", true},
		{"git log **", "git", false},
		{"git log **", "git status", false},
		{"git * **", "git", false},
		{"git * **", "git log", true},
		{"git * **", "git log -n 5", true},

		// The program is never a wildcard, even if a pattern were built by hand.
		{"* status", "git status", false},
		{"*", "git", false},
		{"*", "*", true}, // a program really called "*", spelled out
	}
	split := func(s string) []string {
		if s == "-" {
			return nil
		}
		return strings.Split(s, " ")
	}
	for _, tt := range tests {
		if got := matchArgv(split(tt.pat), split(tt.argv)); got != tt.want {
			t.Errorf("matchArgv(%q, %q) = %v, want %v", tt.pat, tt.argv, got, tt.want)
		}
	}
}

func TestCommand(t *testing.T) {
	p := mustParse(t, "cmd git status\ncmd systemctl is-active *\ncmd make **\ncmd echo 'a b'\n")
	for _, argv := range [][]string{
		{"git", "status"}, {"systemctl", "is-active", "docker"}, {"make"}, {"make", "-j", "8"}, {"echo", "a b"},
		{"systemctl", "is-active", "; rm -rf /"}, // one argument, passed as one argument: no shell reads it
	} {
		if err := p.Command(argv); err != nil {
			t.Errorf("Command(%q) = %v, want allowed", argv, err)
		}
	}
	tests := []struct {
		argv []string
		want string
	}{
		{[]string{"git", "push"}, "the policy does not allow the command: git push. A person can allow it: holdfast allow cmd git push"},
		{[]string{"git"}, "the policy does not allow the command: git. A person can allow it: holdfast allow cmd git"},
		{[]string{"/usr/bin/git", "status"}, "the policy does not allow the command: /usr/bin/git status. A person can allow it: holdfast allow cmd /usr/bin/git status"},
		{[]string{"./git", "status"}, "the policy does not allow the command: ./git status. A person can allow it: holdfast allow cmd ./git status"},
		{[]string{"echo", "a", "b"}, "the policy does not allow the command: echo a b. A person can allow it: holdfast allow cmd echo a b"},
		// The command is shown the way a check writes it; the suggestion is
		// written for the shell it will be pasted into.
		{[]string{"sh", "-c", "echo hi; id"}, `the policy does not allow the command: sh -c "echo hi; id". A person can allow it: holdfast allow cmd sh -c 'echo hi; id'`},
		{[]string{"rm", ""}, `the policy does not allow the command: rm "". A person can allow it: holdfast allow cmd rm ''`},
		{[]string{"echo", "$(id)", "`id`", "$HOME", "*"}, "the policy does not allow the command: echo $(id) `id` $HOME *. A person can allow it: by hand only. The policy reads its argument * as \"anything\", so the rule must be written into the policy file"},
		{[]string{"docker", "ps", "--format=json", "-q"}, "the policy does not allow the command: docker ps --format=json -q. A person can allow it: holdfast allow cmd docker ps --format=json -q"},
	}
	for _, tt := range tests {
		if got := verdict(t, p.Command(tt.argv)); got != tt.want {
			t.Errorf("Command(%q)\n got %q\nwant %q", tt.argv, got, tt.want)
		}
	}
	// The default policy allows no command at all.
	if Default().Command([]string{"true"}) == nil {
		t.Error("the default policy allows a command")
	}
	// A very long command is shown cut short, on one line.
	long := verdict(t, p.Command([]string{"python", "-c", strings.Repeat("x", 500) + "\nimport os"}))
	if strings.ContainsAny(long, "\n\r") || len([]rune(long)) > 330 {
		t.Errorf("a long command is shown as %d characters: %q", len([]rune(long)), long)
	}
}

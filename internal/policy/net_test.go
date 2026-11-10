package policy

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

func mustParse(t *testing.T, text string) *Policy {
	t.Helper()
	p, err := Parse(text)
	if err != nil {
		t.Fatalf("Parse(%q): %v", text, err)
	}
	return p
}

func TestClass(t *testing.T) {
	tests := []struct{ addr, want string }{
		{"127.0.0.1", "loopback"},
		{"127.255.255.254", "loopback"},
		{"::1", "loopback"},
		{"0.0.0.0", "loopback"}, // "this machine": connecting to it reaches a local listener
		{"::", "loopback"},

		{"169.254.169.254", "link-local"}, // cloud metadata
		{"169.254.0.1", "link-local"},
		{"fe80::1", "link-local"},
		{"fe80::1%eth0", "link-local"},
		{"febf::1", "link-local"},

		{"224.0.0.1", "never"},
		{"239.255.255.250", "never"},
		{"ff02::1", "never"},
		{"ff01::1", "never"},
		{"ff0e::1", "never"},
		{"255.255.255.255", "never"},

		{"10.0.0.1", "private"},
		{"10.255.255.255", "private"},
		{"172.16.0.1", "private"},
		{"172.31.255.255", "private"},
		{"192.168.0.1", "private"},
		{"192.168.255.255", "private"},
		{"100.64.0.1", "private"},
		{"100.127.255.255", "private"},
		{"fc00::1", "private"},
		{"fd12:3456:789a::1", "private"},
		{"fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "private"},

		{"8.8.8.8", "public"},
		{"93.184.216.34", "public"},
		{"172.15.255.255", "public"}, // just below 172.16/12
		{"172.32.0.1", "public"},     // just above it
		{"100.63.255.255", "public"}, // just below 100.64/10
		{"100.128.0.1", "public"},    // just above it
		{"192.167.255.255", "public"},
		{"126.255.255.255", "public"},
		{"128.0.0.1", "public"},
		{"2606:4700:4700::1111", "public"},
		{"2001:db8::1", "public"},
		{"fe00::1", "public"}, // not in fc00::/7, not in fe80::/10

		// IPv4 addresses written as IPv6 are sorted as the IPv4 address they are.
		{"::ffff:127.0.0.1", "loopback"},
		{"::ffff:7f00:1", "loopback"},
		{"::ffff:0.0.0.0", "loopback"},
		{"::ffff:169.254.169.254", "link-local"},
		{"::ffff:a9fe:a9fe", "link-local"},
		{"::ffff:10.0.0.1", "private"},
		{"::ffff:172.16.0.1", "private"},
		{"::ffff:192.168.1.1", "private"},
		{"::ffff:100.64.0.1", "private"},
		{"::ffff:224.0.0.1", "never"},
		{"::ffff:255.255.255.255", "never"},
		{"::ffff:8.8.8.8", "public"},
	}
	for _, tt := range tests {
		if got := Class(ip(tt.addr)); got != tt.want {
			t.Errorf("Class(%s) = %s, want %s", tt.addr, got, tt.want)
		}
	}
	if got := Class(netip.Addr{}); got != "never" {
		t.Errorf("Class(no address) = %s, want never", got)
	}
}

func TestBlockedError(t *testing.T) {
	b := &Blocked{Why: "the policy does not allow X", Fix: "holdfast allow net x"}
	if got, want := b.Error(), "the policy does not allow X. A person can allow it: holdfast allow net x"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	b = &Blocked{Why: "Holdfast never connects to 224.0.0.1"}
	if got, want := b.Error(), "Holdfast never connects to 224.0.0.1"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// verdict is "" for allowed, or the exact text of the refusal.
func verdict(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	var b *Blocked
	if !errors.As(err, &b) {
		t.Fatalf("the refusal is a %T, not a *Blocked: %v", err, err)
	}
	return b.Error()
}

const allowed = ""

func TestHostDefaultPolicy(t *testing.T) {
	p := Default()
	tests := []struct{ host, want string }{
		{"localhost", allowed},
		{"LOCALHOST", allowed},
		{"localhost.", allowed},
		{"app.localhost", allowed},
		{"127.0.0.1", allowed},
		{"127.8.9.10", allowed},
		{"::1", allowed},
		{"[::1]", allowed},
		{"0.0.0.0", allowed},
		{"::ffff:127.0.0.1", allowed},

		// A name nobody allowed is refused here, before anything looks it up.
		{"example.com", "the policy does not allow connections to example.com. A person can allow it: holdfast allow net example.com"},
		{"Example.COM.", "the policy does not allow connections to example.com. A person can allow it: holdfast allow net example.com"},
		{"notlocalhost", "the policy does not allow connections to notlocalhost. A person can allow it: holdfast allow net notlocalhost"},
		{"localhost.example.com", "the policy does not allow connections to localhost.example.com. A person can allow it: holdfast allow net localhost.example.com"},
		{"metadata.google.internal", "the policy does not allow connections to metadata.google.internal. A person can allow it: holdfast allow net metadata.google.internal"},
		// Spellings of 127.0.0.1 that are not addresses are names, and refused.
		{"127.1", "the policy does not allow connections to 127.1. A person can allow it: holdfast allow net 127.1"},
		{"2130706433", "the policy does not allow connections to 2130706433. A person can allow it: holdfast allow net 2130706433"},
		{"0x7f.0.0.1", "the policy does not allow connections to 0x7f.0.0.1. A person can allow it: holdfast allow net 0x7f.0.0.1"},

		{"10.0.0.5", "the policy does not allow connections to 10.0.0.5. A person can allow it: holdfast allow net 10.0.0.5"},
		{"192.168.1.1", "the policy does not allow connections to 192.168.1.1. A person can allow it: holdfast allow net 192.168.1.1"},
		{"8.8.8.8", "the policy does not allow connections to 8.8.8.8. A person can allow it: holdfast allow net 8.8.8.8"},
		{"[2001:db8::1]", "the policy does not allow connections to 2001:db8::1. A person can allow it: holdfast allow net 2001:db8::1"},
		{"::ffff:10.0.0.5", "the policy does not allow connections to ::ffff:10.0.0.5. A person can allow it: holdfast allow net 10.0.0.5"},
		{"169.254.169.254", "169.254.169.254 is a link-local address (169.254.169.254), which is only allowed by its exact address. A person can allow it: holdfast allow net 169.254.169.254"},
		{"::ffff:169.254.169.254", "::ffff:169.254.169.254 is a link-local address (169.254.169.254), which is only allowed by its exact address. A person can allow it: holdfast allow net 169.254.169.254"},
		{"fe80::1", "fe80::1 is a link-local address (fe80::1), which is only allowed by its exact address. A person can allow it: holdfast allow net fe80::1"},
		{"224.0.0.1", "Holdfast never connects to 224.0.0.1"},
		{"255.255.255.255", "Holdfast never connects to 255.255.255.255"},
		{"ff02::1", "Holdfast never connects to ff02::1"},
	}
	for _, tt := range tests {
		if got := verdict(t, p.Host(tt.host)); got != tt.want {
			t.Errorf("Host(%q)\n got %q\nwant %q", tt.host, got, tt.want)
		}
	}
}

func TestConnDefaultPolicy(t *testing.T) {
	p := Default()
	tests := []struct{ host, addr, want string }{
		{"localhost", "127.0.0.1", allowed},
		{"localhost", "::1", allowed},
		{"LocalHost.", "127.0.0.1", allowed},
		{"127.0.0.1", "127.0.0.1", allowed},
		{"[::1]", "::1", allowed},
		{"localhost", "::ffff:127.0.0.1", allowed},
		// Any name that really is this machine is fine: the address decides.
		{"example.com", "127.0.0.1", allowed},

		// A name pointed somewhere else is refused by the address it resolved to.
		{"localhost", "10.0.0.5", "the policy does not allow connections to localhost (10.0.0.5, a private address). A person can allow it: holdfast allow net localhost"},
		{"localhost", "93.184.216.34", "the policy does not allow connections to localhost (93.184.216.34, a public address). A person can allow it: holdfast allow net localhost"},
		{"app.localhost", "192.168.1.9", "the policy does not allow connections to app.localhost (192.168.1.9, a private address). A person can allow it: holdfast allow net app.localhost"},
		{"localhost", "169.254.169.254", "localhost is a link-local address (169.254.169.254), which is only allowed by its exact address. A person can allow it: holdfast allow net 169.254.169.254"},
		{"localhost", "::ffff:169.254.169.254", "localhost is a link-local address (169.254.169.254), which is only allowed by its exact address. A person can allow it: holdfast allow net 169.254.169.254"},
		{"localhost", "::ffff:10.0.0.5", "the policy does not allow connections to localhost (10.0.0.5, a private address). A person can allow it: holdfast allow net localhost"},
		{"localhost", "fe80::1", "localhost is a link-local address (fe80::1), which is only allowed by its exact address. A person can allow it: holdfast allow net fe80::1"},
		{"localhost", "224.0.0.1", "Holdfast never connects to 224.0.0.1"},
		{"localhost", "::ffff:224.0.0.1", "Holdfast never connects to 224.0.0.1"},
		{"10.0.0.5", "10.0.0.5", "the policy does not allow connections to 10.0.0.5. A person can allow it: holdfast allow net 10.0.0.5"},
	}
	for _, tt := range tests {
		if got := verdict(t, p.Conn(tt.host, ip(tt.addr))); got != tt.want {
			t.Errorf("Conn(%q, %s)\n got %q\nwant %q", tt.host, tt.addr, got, tt.want)
		}
	}
	if got := verdict(t, p.Conn("localhost", netip.Addr{})); !strings.HasPrefix(got, "Holdfast never connects to") {
		t.Errorf("Conn with no address = %q, want a refusal", got)
	}
}

func TestNothingAllowed(t *testing.T) {
	for _, p := range []*Policy{{}, mustParse(t, "files .\ncmd true\n")} {
		for _, h := range []string{"localhost", "127.0.0.1", "::1", "example.com", "10.0.0.1"} {
			if p.Host(h) == nil {
				t.Errorf("a policy with no net line allows %s", h)
			}
		}
		if p.Conn("localhost", ip("127.0.0.1")) == nil {
			t.Error("a policy with no net line allows a connection to 127.0.0.1")
		}
	}
}

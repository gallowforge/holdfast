package sim

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

// The pretend machine answers local names as a real one does: without
// looking them up, and always with a loopback address.
func TestLocalNamesAreNotLookedUp(t *testing.T) {
	w := machine()
	// What the name table says about a name under .localhost is never read.
	w.Names["evil.localhost"] = []netip.Addr{netip.MustParseAddr("203.0.113.9")}
	for _, name := range []string{"localhost", "LOCALHOST", "localhost.", "anything.localhost", "evil.localhost", "a.b.LocalHost."} {
		ips, err := w.Resolve(ctx, name)
		if got := fmt.Sprint(ips); err != nil || got != "[127.0.0.1]" {
			t.Errorf("Resolve(%q) = %s, %v; want the machine's loopback address", name, got, err)
		}
	}
	for _, name := range []string{"notlocalhost", "localhost.example.com"} {
		if ips, err := w.Resolve(ctx, name); err == nil {
			t.Errorf("Resolve(%q) = %v; want a not-found answer", name, ips)
		}
	}

	w.Ports["api.localhost:9000"] = true
	var a asked
	if err := w.Dial(ctx, "api.localhost:9000", a.allow); err != nil || strings.Join(a, ", ") != "api.localhost 127.0.0.1" {
		t.Errorf("Dial(api.localhost:9000) = %v, asked %q", err, a)
	}

	// A machine put together by hand, with no name table, agrees.
	bare := &World{}
	ips, err := bare.Resolve(ctx, "anything.localhost")
	if got := fmt.Sprint(ips); err != nil || got != "[127.0.0.1 ::1]" {
		t.Errorf("on a machine with no names, Resolve(anything.localhost) = %s, %v", got, err)
	}
}

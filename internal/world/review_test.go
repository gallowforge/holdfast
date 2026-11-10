//go:build unix

package world

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

// noLookup makes the test fail if any name is sent to the system's resolver.
func noLookup(t *testing.T) {
	t.Helper()
	real := lookup
	lookup = func(_ context.Context, _, name string) ([]netip.Addr, error) {
		t.Errorf("%q was sent to the resolver", name)
		return nil, errors.New("this test has no resolver")
	}
	t.Cleanup(func() { lookup = real })
}

// localhost and the names under .localhost stand for this machine. A resolver
// may pass such a name on to a DNS server, so none of them is ever looked up.
func TestLocalNamesAreAnsweredWithoutALookup(t *testing.T) {
	noLookup(t)
	o := &OS{}
	ctx := context.Background()
	for _, name := range []string{"localhost", "LOCALHOST", "localhost.", "anything.localhost", "a.b.LocalHost."} {
		ips, err := o.Resolve(ctx, name)
		if got := fmt.Sprint(ips); err != nil || got != "[127.0.0.1 ::1]" {
			t.Errorf("Resolve(%q) = %s, %v; want both loopback addresses", name, got, err)
		}
	}

	ln, port := listen(t)
	w := &watch{refuse: only4}
	if err := o.Dial(ctx, "anything.localhost:"+port, w.guard); err != nil {
		t.Fatalf("Dial(anything.localhost): %v", err)
	}
	nextPeer(t, ln) // the connection really arrived
	if got := strings.Join(w.saw(), ", "); got != "anything.localhost 127.0.0.1" {
		t.Errorf("the guard was asked: %s", got)
	}

	s := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "here") })
	status, body, err := o.Get(ctx, s.as("api.localhost", "/"), (&watch{refuse: only4}).guard, 100)
	if err != nil || status != 200 || string(body) != "here" {
		t.Errorf("Get(api.localhost) = %d, %q, %v", status, body, err)
	}
}

// Every other name still goes to the resolver, and its answer is used.
func TestOtherNamesAreLookedUp(t *testing.T) {
	real := lookup
	t.Cleanup(func() { lookup = real })
	var asked []string
	lookup = func(_ context.Context, _, name string) ([]netip.Addr, error) {
		asked = append(asked, name)
		return []netip.Addr{netip.MustParseAddr("::ffff:192.0.2.7")}, nil
	}
	o := &OS{}
	for _, name := range []string{"example.com", "notlocalhost", "localhost.example.com"} {
		ips, err := o.Resolve(context.Background(), name)
		if got := fmt.Sprint(ips); err != nil || got != "[192.0.2.7]" {
			t.Errorf("Resolve(%q) = %s, %v", name, got, err)
		}
	}
	if got := strings.Join(asked, " "); got != "example.com notlocalhost localhost.example.com" {
		t.Errorf("the resolver was asked: %s", got)
	}
}

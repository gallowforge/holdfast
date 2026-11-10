package proof

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/sim"
	"github.com/gallowforge/holdfast/internal/world"
)

// Storing an "unchanged" fact takes the file's fingerprint. From then on any
// change to the file, or its removal, breaks the fact.
func TestSeal(t *testing.T) {
	w, p := machine(), open()
	c := parse(t, "unchanged LICENSE")
	if err := Seal(bg, w, p, c); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	was := sum("MIT")
	if c.Sum != was || c.String() != "unchanged LICENSE "+was {
		t.Fatalf("after Seal the check is %s, want the sha256 of the file (%s)", c.String(), was)
	}
	not := *c
	not.Not = true
	run := func(step string, status fact.Status, detail string) {
		t.Helper()
		opposite := fact.Holds
		if status == fact.Holds {
			opposite = fact.Broken
		}
		if ev := Run(bg, w, p, c); ev.Status != status || ev.Detail != detail {
			t.Errorf("%s\n got %s: %s\nwant %s: %s", step, ev.Status, ev.Detail, status, detail)
		}
		if ev := Run(bg, w, p, &not); ev.Status != opposite || ev.Detail != detail {
			t.Errorf("%s, with not\n got %s: %s\nwant %s: %s", step, ev.Status, ev.Detail, opposite, detail)
		}
	}
	short := func(s string) string { return s[:8] + "…" + s[60:] }

	run("as stored", fact.Holds, "LICENSE is unchanged (sha256 "+short(was)+")")
	// Sealing again keeps the fingerprint of the day the fact was stored.
	w.Change(func(w *sim.World) { w.Write("LICENSE", "MIT ", noon) })
	if err := Seal(bg, w, p, c); err != nil || c.Sum != was {
		t.Errorf("a second Seal = %v, fingerprint %s; want it left alone", err, c.Sum)
	}
	run("one space added", fact.Broken, "LICENSE has changed (sha256 was "+short(was)+", is now "+short(sum("MIT "))+")")
	w.Change(func(w *sim.World) { w.Write("LICENSE", "", noon) })
	run("emptied", fact.Broken, "LICENSE has changed (sha256 was "+short(was)+", is now e3b0c442…b855)")
	w.Change(func(w *sim.World) { delete(w.Files, "LICENSE") })
	run("deleted", fact.Broken, "LICENSE does not exist any more")
	w.Change(func(w *sim.World) { w.Write("LICENSE", "MIT", noon.Add(time.Hour)) })
	run("put back, with a new date", fact.Holds, "LICENSE is unchanged (sha256 "+short(was)+")")

	// "not unchanged" is sealed the same way.
	c = parse(t, "not unchanged go.mod")
	if err := Seal(bg, w, p, c); err != nil || c.Sum != sum("module example\n\ngo 1.24\n") || !c.Not {
		t.Errorf("Seal(not unchanged go.mod) = %v, %s", err, c.String())
	}
	// An empty file has a fingerprint too.
	w.Write("empty", "", noon)
	c = parse(t, "unchanged empty")
	if err := Seal(bg, w, p, c); err != nil || !strings.HasPrefix(c.Sum, "e3b0c442") {
		t.Errorf("Seal(unchanged empty) = %v, %s", err, c.String())
	}
}

func TestSealRefuses(t *testing.T) {
	w, p := machine(), open()
	tests := []struct {
		w       world.World
		p       *policy.Policy
		path    string
		want    string
		blocked bool
	}{
		{w, p, "missing.txt", "missing.txt does not exist, so there is nothing to keep unchanged", false},
		{w, p, "backups/missing.tar", "backups/missing.tar does not exist, so there is nothing to keep unchanged", false},
		{w, p, "backups", "backups is a folder, not a file", true},
		{w, p, "../outside", "../outside is outside the folders the policy allows. A person can allow it: holdfast allow files ..", true},
		{w, &policy.Policy{}, "LICENSE", "LICENSE is outside the folders the policy allows. A person can allow it: holdfast allow files .", true},
		{&fake{World: w, stat: func(string) (world.FileInfo, error) {
			return world.FileInfo{Exists: true, Regular: true, Size: MaxHash + 1}, nil
		}}, p, "disk.img", "disk.img is larger than 1 GiB", true},
		{&fake{World: w, open: func(string) (io.ReadCloser, error) {
			return nil, errors.New("open /srv/project/LICENSE: permission denied")
		}}, p, "LICENSE", "cannot read LICENSE: permission denied", true},
	}
	for _, tt := range tests {
		c := &fact.Check{Kind: "unchanged", Path: tt.path}
		err := Seal(bg, tt.w, tt.p, c)
		if err == nil || err.Error() != tt.want || c.Sum != "" {
			t.Errorf("Seal(unchanged %s) = %v, fingerprint %q\nwant %s", tt.path, err, c.Sum, tt.want)
			continue
		}
		var b *policy.Blocked
		if errors.As(err, &b) != tt.blocked {
			t.Errorf("Seal(unchanged %s): the error is a policy refusal: %v, want %v", tt.path, !tt.blocked, tt.blocked)
		}
	}
	// A caller that has given up gets an error, not half a fingerprint.
	ctx, cancel := context.WithCancel(bg)
	cancel()
	c := parse(t, "unchanged LICENSE")
	if err := Seal(ctx, w, p, c); err == nil || c.Sum != "" {
		t.Errorf("Seal with a context that is over = %v, fingerprint %q", err, c.Sum)
	}
}

// Only "unchanged" has something to remember. Every other check is left as
// it is, and nothing on the machine is looked at.
func TestSealLeavesOtherChecksAlone(t *testing.T) {
	w := &fake{World: machine(),
		stat: func(string) (world.FileInfo, error) { t.Error("Seal looked at a file"); return world.FileInfo{}, nil },
	}
	for _, line := range []string{
		"file go.mod", "contains go.mod module", "matches go.mod ^go", "fresh go.mod 1h", "not file /etc/passwd",
		"http 200 https://example.com/", "port localhost:8080", "dns example.com", "cmd true", "says ok true",
		"unchanged LICENSE " + strings.Repeat("ab", 32),
	} {
		c := parse(t, line)
		before := *c.Clone()
		if err := Seal(bg, w, &policy.Policy{}, c); err != nil || !reflect.DeepEqual(*c, before) {
			t.Errorf("Seal(%s) = %v, check now %s", line, err, c.String())
		}
	}
}

// A check has five seconds, a command ten. Waiting those out here would make
// the tests slow, so the time is already up when these start, and the part of
// the prover that words the answer is asked directly.
func TestNoAnswerInTime(t *testing.T) {
	ctx, cancel := context.WithDeadline(bg, time.Now().Add(-time.Second))
	defer cancel()
	late := func(ctx context.Context) error { return &net.OpError{Op: "dial", Net: "tcp", Err: ctx.Err()} }
	w := &fake{World: machine(),
		get:     func(ctx context.Context) (int, []byte, error) { return 0, nil, late(ctx) },
		dial:    late,
		resolve: func(ctx context.Context) ([]netip.Addr, error) { return nil, late(ctx) },
		run:     func(ctx context.Context) (int, []byte, error) { return -1, []byte("partial"), ctx.Err() },
	}
	tests := []struct {
		line   string
		limit  time.Duration
		detail string
	}{
		{"http 200 https://example.com/?q=" + querySecret, Timeout, "https://example.com/?… did not answer: no answer within 5s"},
		{"port db.internal:5432", Timeout, "db.internal:5432 does not accept connections: no answer within 5s"},
		{"dns example.com", Timeout, "example.com does not resolve: no answer within 5s"},
		{"cmd systemctl is-active docker", CommandTimeout, "systemctl is-active docker did not finish within 10s"},
		{"says active systemctl is-active docker", CommandTimeout, "systemctl is-active docker did not finish within 10s"},
	}
	for _, tt := range tests {
		ok, detail, err := observe(ctx, w, open(), parse(t, tt.line), tt.limit)
		if ok || err != nil || detail != tt.detail {
			t.Errorf("%s = %v, %q, %v\nwant %q", tt.line, ok, detail, err, tt.detail)
		}
	}
	// A fingerprint that cannot be finished in time is not a changed file.
	ok, detail, err := observe(ctx, w, open(), parse(t, "unchanged LICENSE "+sum("MIT")), Timeout)
	if ok || err == nil || err.Error() != "reading LICENSE took too long" {
		t.Errorf("unchanged = %v, %q, %v", ok, detail, err)
	}

	// The world may also report its own timeout while the check still has time.
	slow := &fake{World: machine(), dial: func(context.Context) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: context.DeadlineExceeded}
	}}
	ev := Run(bg, slow, open(), parse(t, "port db.internal:5432"))
	if ev.Status != fact.Broken || ev.Detail != "db.internal:5432 does not accept connections: no answer within 5s" {
		t.Errorf("port = %s: %s", ev.Status, ev.Detail)
	}
}

// A caller that stops waiting (a shutdown, a spent budget) cuts the check
// short. What the check saw while being stopped is not evidence: it must not
// break a fact, and it must not make a "not" fact hold.
func TestStoppedByTheCaller(t *testing.T) {
	stopped := func(ctx context.Context) error { return &net.OpError{Op: "dial", Net: "tcp", Err: ctx.Err()} }
	w := &fake{World: machine(),
		get:     func(ctx context.Context) (int, []byte, error) { return 0, nil, stopped(ctx) },
		dial:    stopped,
		resolve: func(ctx context.Context) ([]netip.Addr, error) { return nil, stopped(ctx) },
		run:     func(ctx context.Context) (int, []byte, error) { return -1, nil, ctx.Err() },
	}
	cancelled, cancel := context.WithCancel(bg)
	cancel()
	spent, cancel2 := context.WithDeadline(bg, time.Now().Add(-time.Second))
	defer cancel2()
	const want = "could not check: the check was stopped before it finished"
	for _, ctx := range []context.Context{cancelled, spent} {
		for _, line := range []string{
			"http 200 http://localhost:8080/healthz", "port localhost:8080", "dns example.com",
			"cmd systemctl is-active docker", "says active systemctl is-active docker",
		} {
			for _, not := range []string{"", "not "} {
				ev := Run(ctx, w, open(), parse(t, not+line))
				if ev.Status != fact.Blocked || ev.Detail != want {
					t.Errorf("%s%s under %v\n got %s: %s\nwant blocked: %s", not, line, ctx.Err(), ev.Status, ev.Detail, want)
				}
			}
		}
	}
	// A plain yes that was seen in full stands, stopped or not.
	sure := machine()
	for _, line := range []string{"file go.mod", "port localhost:8080", "cmd true"} {
		if ev := Run(cancelled, sure, open(), parse(t, line)); ev.Status != fact.Holds {
			t.Errorf("%s under a stopped caller = %s: %s", line, ev.Status, ev.Detail)
		}
	}
}

func TestSpan(t *testing.T) {
	const day = 24 * time.Hour
	tests := []struct {
		d    time.Duration
		want string
	}{
		{-time.Hour, "0s"},
		{0, "0s"},
		{999 * time.Millisecond, "0s"},
		{42 * time.Second, "42s"},
		{59*time.Second + 999*time.Millisecond, "59s"},
		{time.Minute, "1m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h"},
		{time.Hour + 59*time.Second, "1h"},
		{3*time.Hour + 10*time.Minute, "3h 10m"},
		{23*time.Hour + 59*time.Minute, "23h 59m"},
		{day, "1d"},
		{day + 59*time.Minute, "1d"},
		{3*day + 4*time.Hour, "3d 4h"},
		{400*day + 23*time.Hour + 59*time.Minute, "400d 23h"},
	}
	for _, tt := range tests {
		if got := span(tt.d); got != tt.want {
			t.Errorf("span(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestShown(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://example.com", "https://example.com"},
		{"https://example.com/", "https://example.com/"},
		{"https://example.com/a/b.html", "https://example.com/a/b.html"},
		{"http://localhost:8080/healthz?verbose=1", "http://localhost:8080/healthz?…"},
		{"http://localhost:8080/healthz?token=" + querySecret + "&a=b#" + querySecret, "http://localhost:8080/healthz?…"},
		{"http://localhost:8080/#" + querySecret, "http://localhost:8080/"},
		{"http://localhost:8080?" + querySecret, "http://localhost:8080?…"},
		{"https://alex:" + querySecret + "@example.com/a?b", "https://example.com/a?…"},
		{"https://[2001:db8::1]:8443/a%20b?c", "https://[2001:db8::1]:8443/a%20b?…"},
		{"https://example.com/a?", "https://example.com/a?"}, // an empty query has nothing to hide
	}
	for _, tt := range tests {
		u, err := url.Parse(tt.in)
		if err != nil {
			t.Fatal(err)
		}
		before := u.String()
		if got := shown(u); got != tt.want {
			t.Errorf("shown(%s) = %s, want %s", tt.in, got, tt.want)
		}
		if u.String() != before {
			t.Errorf("shown changed the address itself: %s", u)
		}
	}
}

func TestSmallHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "",
		"abc":                    "abc",
		"0123456789a":            "0123456789a",
		"0123456789ab":           "01234567…89ab",
		strings.Repeat("0f", 32): "0f0f0f0f…0f0f",
	} {
		if got := short(in); got != want {
			t.Errorf("short(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"":                            "",
		"\n\n \t\n":                   "",
		"one":                         "one",
		"one\ntwo":                    "one",
		"\r\n\n  two  words \r\nmore": "two words",
		"\x1b[1mbold\x1b[0m\n":        "[1mbold[0m",
		"\x00\x07\n real":             "real",
		strings.Repeat("x", 500):      strings.Repeat("x", 119) + "…",
	} {
		if got := firstLine([]byte(in)); got != want {
			t.Errorf("firstLine(%q) = %q, want %q", in, got, want)
		}
	}
	if got := quote("a\n\tb  " + strings.Repeat("c", 100)); got != `"a b `+strings.Repeat("c", 55)+`…"` {
		t.Errorf("quote = %s", got)
	}
}

// Why a connection failed, in a few plain words, without whatever the other
// side sent.
func TestNetErr(t *testing.T) {
	const banner = "SSH-2.0-" + pageSecret
	tests := []struct {
		err  error
		want string
	}{
		{&net.DNSError{Err: "no such host", Name: "nowhere.test", IsNotFound: true}, "the name does not exist"},
		{&net.DNSError{Err: "server misbehaving", Name: "example.com", Server: "192.168.1.1:53"}, "the name could not be looked up"},
		{&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "x.test", IsNotFound: true}}, "the name does not exist"},
		{context.DeadlineExceeded, "no answer within 5s"},
		{errors.New("dial tcp 127.0.0.1:9: connect: connection refused"), "connection refused"},
		{errors.New("dial tcp 10.9.9.9:80: connect: no route to host"), "no route to host"},
		{errors.New("dial tcp [2001:db8::1]:80: connect: network is unreachable"), "network is unreachable"},
		{errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"), "the TLS certificate was not accepted"},
		{errors.New("tls: failed to verify certificate: x509: certificate is valid for internal.corp.test, not example.com"), "the TLS certificate was not accepted"},
		{errors.New("read tcp 127.0.0.1:5000->127.0.0.1:80: read: connection reset by peer"), "connection reset"},
		{errors.New("EOF"), "EOF"},
		{errors.New("http: server gave HTTP response to HTTPS client"), "server gave HTTP response to HTTPS client"},
		{errors.New("tls: first record does not look like a TLS handshake"), "first record does not look like a TLS handshake"},
		{errors.New("more than 5 redirects"), "more than 5 redirects"},
		{errors.New("a redirect carried credentials"), "a redirect carried credentials"},
		{errors.New("db.internal has no addresses"), "db.internal has no addresses"},
		// The standard library quotes what the other side sent. None of it is kept.
		{errors.New(`net/http: HTTP/1.x transport connection broken: malformed HTTP response "` + banner + `"`), "malformed HTTP response"},
		{errors.New(`net/http: HTTP/1.x transport connection broken: malformed HTTP status code "` + pageSecret + `"`), "malformed HTTP status code"},
		{errors.New(`net/http: HTTP/1.x transport connection broken: malformed MIME header: missing colon: "` + pageSecret + `"`), "missing colon"},
		{errors.New(`net/http: HTTP/1.x transport connection broken: malformed MIME header line: "` + pageSecret + `: x"`), "malformed MIME header line"},
		{errors.New(`failed to parse Location header "http://u:` + pageSecret + `@h/": net/url: invalid userinfo`), "failed to parse Location header"},
		{errors.New(`bad Content-Length "` + pageSecret + `"`), "bad Content-Length"},
		{errors.New(`unsupported protocol scheme "` + pageSecret + `"`), "unsupported protocol scheme"},
		{errors.New(`"` + pageSecret + `" went wrong`), "the connection failed"},
	}
	for _, tt := range tests {
		got := netErr(bg, tt.err, Timeout)
		if got != tt.want {
			t.Errorf("netErr(%v) = %q, want %q", tt.err, got, tt.want)
		}
		if strings.Contains(got, pageSecret) {
			t.Errorf("netErr(%v) repeats what the other side sent", tt.err)
		}
	}
	// When the check's own time is up, that is the reason, whatever the error says.
	ctx, cancel := context.WithDeadline(bg, time.Now().Add(-time.Second))
	defer cancel()
	if got := netErr(ctx, errors.New("dial tcp 127.0.0.1:9: operation was canceled"), CommandTimeout); got != "no answer within 10s" {
		t.Errorf("netErr under a spent deadline = %q", got)
	}
}

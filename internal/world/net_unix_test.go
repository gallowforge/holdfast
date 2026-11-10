//go:build unix

package world

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/policy"
)

// watch is a guard that writes down what it was asked and refuses what the
// test tells it to.
type watch struct {
	mu     sync.Mutex
	asked  []string // "host ip"
	refuse func(host string, ip netip.Addr) bool
}

func (w *watch) guard(host string, ip netip.Addr) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.asked = append(w.asked, host+" "+ip.String())
	if w.refuse != nil && w.refuse(host, ip) {
		return &policy.Blocked{Why: "the test does not allow " + host, Fix: "nothing"}
	}
	return nil
}

func (w *watch) saw() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.asked...)
}

// only4 keeps a name such as localhost on the IPv4 listener the test opened.
func only4(_ string, ip netip.Addr) bool { return !ip.Is4() }

func listen(t *testing.T) (ln net.Listener, port string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	return ln, port
}

// nextPeer accepts the next connection in the queue and says who made it.
func nextPeer(t *testing.T, ln net.Listener) string {
	t.Helper()
	ln.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Second))
	c, err := ln.Accept()
	if err != nil {
		t.Fatalf("nothing connected: %v", err)
	}
	defer c.Close()
	return c.RemoteAddr().String()
}

// knock connects to the listener from the test itself and returns the
// address it connected from.
func knock(t *testing.T, ln net.Listener) string {
	t.Helper()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c.LocalAddr().String()
}

func TestDialAsksTheGuardAboutTheRealAddress(t *testing.T) {
	o := &OS{}
	ln, port := listen(t)
	ctx := context.Background()

	tests := []struct {
		addr string
		want string // what the guard must be asked
	}{
		{"127.0.0.1:" + port, "127.0.0.1 127.0.0.1"},
		{"[::ffff:127.0.0.1]:" + port, "::ffff:127.0.0.1 127.0.0.1"}, // the address is given unmapped
		{"localhost:" + port, "localhost 127.0.0.1"},                 // the name is resolved by the dialler
		{"LOCALHOST:" + port, "LOCALHOST 127.0.0.1"},
	}
	for _, tt := range tests {
		w := &watch{refuse: only4}
		if err := o.Dial(ctx, tt.addr, w.guard); err != nil {
			t.Errorf("Dial(%s): %v", tt.addr, err)
			continue
		}
		nextPeer(t, ln) // the connection really arrived
		found := false
		for _, a := range w.saw() {
			host, ip, _ := strings.Cut(a, " ")
			if addr, err := netip.ParseAddr(ip); err != nil || !addr.IsLoopback() || addr.Is4In6() {
				t.Errorf("Dial(%s): the guard was asked about %q, not a plain loopback address", tt.addr, a)
			}
			if wantHost, _, _ := strings.Cut(tt.want, " "); host != wantHost {
				t.Errorf("Dial(%s): the guard was asked about host %q, want %q", tt.addr, host, wantHost)
			}
			found = found || a == tt.want
		}
		if !found {
			t.Errorf("Dial(%s): the guard was asked %q, want %q among them", tt.addr, w.saw(), tt.want)
		}
	}
}

func TestDialRefusedMeansNoConnection(t *testing.T) {
	o := &OS{}
	ln, port := listen(t)
	for _, addr := range []string{"127.0.0.1:" + port, "localhost:" + port, "[::ffff:127.0.0.1]:" + port} {
		w := &watch{refuse: func(string, netip.Addr) bool { return true }}
		err := o.Dial(context.Background(), addr, w.guard)
		if b := blocked(err); b == nil || !strings.HasPrefix(b.Why, "the test does not allow ") {
			t.Errorf("Dial(%s) = %v, want the guard's refusal", addr, err)
		}
		if len(w.saw()) == 0 {
			t.Errorf("Dial(%s): the guard was never asked", addr)
		}
	}
	// Connections are accepted in the order they were made. The next one in
	// the queue is the test's own, so none of the refused dials got through.
	me := knock(t, ln)
	if peer := nextPeer(t, ln); peer != me {
		t.Errorf("a refused dial still connected (from %s)", peer)
	}
}

func TestDialErrors(t *testing.T) {
	o := &OS{}
	allow := (&watch{}).guard

	ln, port := listen(t)
	ln.Close()
	err := o.Dial(context.Background(), "127.0.0.1:"+port, allow)
	if err == nil || blocked(err) != nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("Dial to a closed port = %v, want connection refused", err)
	}

	for _, addr := range []string{"127.0.0.1", "", "no-port", "127.0.0.1:80:80"} {
		w := &watch{}
		if err := o.Dial(context.Background(), addr, w.guard); err == nil || len(w.saw()) != 0 {
			t.Errorf("Dial(%q) = %v after asking %q; want an error before anything is asked", addr, err, w.saw())
		}
	}

	// A context that is already over: nothing is connected.
	ln2, port2 := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := o.Dial(ctx, "127.0.0.1:"+port2, allow); err == nil {
		t.Error("Dial with a cancelled context succeeded")
	}
	me := knock(t, ln2)
	if peer := nextPeer(t, ln2); peer != me {
		t.Errorf("a cancelled dial still connected (from %s)", peer)
	}
}

func TestResolve(t *testing.T) {
	o := &OS{}
	ips, err := o.Resolve(context.Background(), "localhost")
	if err != nil || len(ips) == 0 {
		t.Fatalf("Resolve(localhost) = %v, %v", ips, err)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() || ip.Is4In6() {
			t.Errorf("Resolve(localhost) gave %s", ip)
		}
	}
	if ips, err := o.Resolve(context.Background(), "127.0.0.1"); err != nil || len(ips) != 1 || ips[0] != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("Resolve(127.0.0.1) = %v, %v", ips, err)
	}
	if ips, err := o.Resolve(context.Background(), "::ffff:127.0.0.1"); err != nil || len(ips) != 1 || ips[0] != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("Resolve(::ffff:127.0.0.1) = %v, %v; want it unmapped", ips, err)
	}
}

// site is a test web server that counts what reaches it.
type site struct {
	*httptest.Server
	port string
	hits atomic.Int64
	last atomic.Pointer[http.Request]
}

func serve(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *site {
	t.Helper()
	s := &site{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.last.Store(r)
		h(w, r)
	}))
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil || u.Hostname() != "127.0.0.1" {
		t.Skipf("the test server is not on 127.0.0.1: %s", s.URL)
	}
	s.port = u.Port()
	return s
}

func (s *site) as(host, path string) string { return "http://" + host + ":" + s.port + path }

func TestGetStatusAndBody(t *testing.T) {
	const page = "0123456789abcdef"
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, page)
		case "/teapot":
			w.WriteHeader(http.StatusTeapot)
			fmt.Fprint(w, "short and stout")
		case "/big":
			w.Write(make([]byte, 3<<20))
		default:
			http.NotFound(w, r)
		}
	})
	o := &OS{UserAgent: "holdfast-test/1"}
	ctx := context.Background()
	tests := []struct {
		path   string
		max    int64
		status int
		body   string
	}{
		{"/", 1 << 20, 200, page},
		{"/", int64(len(page)), 200, page},
		{"/", 4, 200, "0123"},
		{"/", 1, 200, "0"},
		{"/", 0, 200, ""},  // nothing is read at all
		{"/", -1, 200, ""}, // nor with a nonsense limit
		{"/teapot", 100, 418, "short and stout"},
		{"/missing", 0, 404, ""},
	}
	for _, tt := range tests {
		w := &watch{}
		status, body, err := o.Get(ctx, s.as("127.0.0.1", tt.path), w.guard, tt.max)
		if err != nil || status != tt.status || string(body) != tt.body {
			t.Errorf("Get(%s, max %d) = %d, %q, %v; want %d, %q", tt.path, tt.max, status, body, err, tt.status, tt.body)
		}
		if got := w.saw(); len(got) != 1 || got[0] != "127.0.0.1 127.0.0.1" {
			t.Errorf("Get(%s): the guard was asked %q", tt.path, got)
		}
	}
	status, body, err := o.Get(ctx, s.as("127.0.0.1", "/big"), (&watch{}).guard, 1<<20)
	if err != nil || status != 200 || len(body) != 1<<20 {
		t.Errorf("Get(/big) = %d, %d bytes, %v; want exactly the limit", status, len(body), err)
	}

	// What the server saw of the client.
	r := s.last.Load()
	if got := r.Header.Get("User-Agent"); got != "holdfast-test/1" {
		t.Errorf("User-Agent = %q", got)
	}
	if got := r.Header.Get("Accept"); got != "*/*" {
		t.Errorf("Accept = %q", got)
	}
	if r.Method != http.MethodGet {
		t.Errorf("method = %s", r.Method)
	}
	for _, h := range []string{"Cookie", "Authorization", "Referer"} {
		if got := r.Header.Get(h); got != "" {
			t.Errorf("the request carried %s: %q", h, got)
		}
	}
}

func TestGetErrors(t *testing.T) {
	o := &OS{}
	allow := (&watch{}).guard
	ctx := context.Background()

	for _, raw := range []string{"", "127.0.0.1", "ftp://127.0.0.1/", "file:///etc/passwd", "http://", "http://[::1", "\x00"} {
		w := &watch{}
		if status, body, err := o.Get(ctx, raw, w.guard, 100); err == nil || len(w.saw()) != 0 {
			t.Errorf("Get(%q) = %d, %q, %v after asking %q; want an error and no connection", raw, status, body, err, w.saw())
		}
	}

	ln, port := listen(t)
	ln.Close()
	_, _, err := o.Get(ctx, "http://127.0.0.1:"+port+"/", allow, 100)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("Get from a closed port = %v", err)
	}

	// Refused by the guard: the server is never reached, and the refusal can
	// be told apart from an ordinary failure.
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, secret) })
	w := &watch{refuse: func(string, netip.Addr) bool { return true }}
	status, body, err := o.Get(ctx, s.URL, w.guard, 100)
	if blocked(err) == nil || status != 0 || len(body) != 0 {
		t.Errorf("Get through a refusing guard = %d, %q, %v", status, body, err)
	}
	if s.hits.Load() != 0 {
		t.Error("a refused request reached the server")
	}

	// A server that takes too long: the caller's deadline ends the wait.
	release := make(chan struct{})
	slow := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err = o.Get(short, slow.URL, allow, 100)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Get from a slow server = %v, want the deadline", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Get waited %v past a 100ms deadline", d)
	}
}

// A certificate that no authority vouches for is not accepted.
func TestGetDoesNotTrustAnUnknownCertificate(t *testing.T) {
	var hits atomic.Int64
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, secret)
	}))
	s.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshake is expected
	s.StartTLS()
	defer s.Close()
	status, body, err := (&OS{}).Get(context.Background(), s.URL, (&watch{}).guard, 100)
	if err == nil || !strings.Contains(err.Error(), "certificate") || status != 0 || len(body) != 0 {
		t.Errorf("Get = %d, %q, %v; want a certificate error", status, body, err)
	}
	if hits.Load() != 0 {
		t.Error("the request was sent over a connection that was not trusted")
	}
}

// A proxy would hide the real destination from the guard, so the proxy
// settings of the environment are not used.
func TestGetIgnoresProxySettings(t *testing.T) {
	// Nothing listens on port 1: a request sent to this proxy fails.
	const dead = "http://127.0.0.1:1"
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(k, dead)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	s := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "direct") })
	o := &OS{}
	ctx := context.Background()
	for _, host := range []string{"127.0.0.1", "localhost"} {
		w := &watch{refuse: only4}
		status, body, err := o.Get(ctx, s.as(host, "/"), w.guard, 100)
		if err != nil || status != 200 || string(body) != "direct" {
			t.Errorf("Get(%s) with a dead proxy configured = %d, %q, %v", host, status, body, err)
		}
		for _, a := range w.saw() {
			if !strings.HasPrefix(a, host+" ") {
				t.Errorf("the guard was asked about %q, not about the destination", a)
			}
		}
	}

	// Go never sends requests for loopback addresses to a proxy, so the above
	// would pass either way. 0.0.0.0 is not a loopback address by its
	// spelling, yet on this kind of machine it reaches the same listener: a
	// client that honours the environment fails here, and Get must not.
	target := s.as("0.0.0.0", "/")
	if c, err := net.DialTimeout("tcp", "0.0.0.0:"+s.port, 5*time.Second); err != nil {
		t.Logf("0.0.0.0 does not reach the local listener here (%v); the stronger half is skipped", err)
		return
	} else {
		c.Close()
	}
	env := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true}, Timeout: 10 * time.Second}
	if resp, err := env.Get(target); err == nil {
		resp.Body.Close()
		t.Log("a client that reads the environment was not sent to the proxy; the stronger half proves nothing here")
	}
	status, body, err := o.Get(ctx, target, (&watch{}).guard, 100)
	if err != nil || status != 200 || string(body) != "direct" {
		t.Errorf("Get(%s) with a dead proxy configured = %d, %q, %v", target, status, body, err)
	}
}

func TestGetRedirects(t *testing.T) {
	inner := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, secret) })
	var outer *site
	outer = serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-name":
			http.Redirect(w, r, inner.as("localhost", "/x"), http.StatusFound)
		case "/to-address":
			http.Redirect(w, r, inner.as("127.0.0.1", "/x"), http.StatusMovedPermanently)
		case "/with-password":
			http.Redirect(w, r, "http://user:pass@127.0.0.1:"+inner.port+"/x", http.StatusFound)
		case "/with-user":
			http.Redirect(w, r, "http://user@127.0.0.1:"+inner.port+"/x", http.StatusFound)
		case "/to-file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case "/end":
			fmt.Fprint(w, "the end")
		default:
			// /hop/3 leads to /hop/2, then /hop/1, then /end.
			var n int
			fmt.Sscanf(r.URL.Path, "/hop/%d", &n)
			if n <= 1 {
				http.Redirect(w, r, "/end", http.StatusFound)
			} else {
				http.Redirect(w, r, fmt.Sprintf("/hop/%d", n-1), http.StatusFound)
			}
		}
	})
	o := &OS{}
	ctx := context.Background()
	byName := func(name string) func(string, netip.Addr) bool {
		return func(host string, ip netip.Addr) bool { return host == name || !ip.Is4() }
	}

	// An allowed redirect is followed, and the guard is asked about each hop.
	w := &watch{refuse: only4}
	status, body, err := o.Get(ctx, outer.as("127.0.0.1", "/to-name"), w.guard, 100)
	if err != nil || status != 200 || string(body) != secret {
		t.Errorf("an allowed redirect = %d, %q, %v", status, body, err)
	}
	if got := strings.Join(w.saw(), ", "); !strings.HasPrefix(got, "127.0.0.1 127.0.0.1, localhost ") {
		t.Errorf("the guard was asked: %s", got)
	}

	// A redirect to a host the guard refuses stops there. The two servers are
	// told apart by how they are named: 127.0.0.1 and localhost.
	for _, tt := range []struct{ from, path, refused string }{
		{"127.0.0.1", "/to-name", "localhost"},
		{"localhost", "/to-address", "127.0.0.1"},
	} {
		inner.hits.Store(0)
		outer.hits.Store(0)
		w := &watch{refuse: byName(tt.refused)}
		status, body, err := o.Get(ctx, outer.as(tt.from, tt.path), w.guard, 100)
		if b := blocked(err); b == nil || b.Why != "the test does not allow "+tt.refused {
			t.Errorf("redirect from %s to %s = %d, %q, %v; want the guard's refusal", tt.from, tt.refused, status, body, err)
		}
		if status != 0 || len(body) != 0 || (err != nil && strings.Contains(err.Error(), secret)) {
			t.Errorf("redirect from %s to %s let something out: %d, %q, %v", tt.from, tt.refused, status, body, err)
		}
		if outer.hits.Load() != 1 || inner.hits.Load() != 0 {
			t.Errorf("redirect from %s to %s: first server saw %d requests, refused server saw %d", tt.from, tt.refused, outer.hits.Load(), inner.hits.Load())
		}
	}

	// An error after a redirect does not repeat the address the server sent.
	leaky := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/reset?token="+secret, http.StatusFound)
	})
	if _, _, err := o.Get(ctx, leaky.URL, (&watch{}).guard, 100); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("a failed redirect is reported as: %v", err)
	}

	// Five redirects are followed; a sixth is one too many.
	if maxRedirects != 5 {
		t.Fatalf("maxRedirects = %d; this test was written for 5", maxRedirects)
	}
	for hops, wantErr := range map[int]string{0: "", 1: "", 5: "", 6: "more than 5 redirects", 7: "more than 5 redirects", 50: "more than 5 redirects"} {
		path := "/end"
		if hops > 0 {
			path = fmt.Sprintf("/hop/%d", hops)
		}
		outer.hits.Store(0)
		status, body, err := o.Get(ctx, outer.as("127.0.0.1", path), (&watch{}).guard, 100)
		switch {
		case wantErr == "" && (err != nil || status != 200 || string(body) != "the end"):
			t.Errorf("%d redirects = %d, %q, %v; want them followed", hops, status, body, err)
		case wantErr != "" && (err == nil || !strings.HasSuffix(err.Error(), wantErr) || status != 0):
			t.Errorf("%d redirects = %d, %q, %v; want %q", hops, status, body, err, wantErr)
		}
		if n := outer.hits.Load(); n > 6 {
			t.Errorf("%d redirects: %d requests were sent", hops, n)
		}
	}

	// A redirect that carries a user name or a password is not followed.
	for _, path := range []string{"/with-password", "/with-user"} {
		inner.hits.Store(0)
		status, body, err := o.Get(ctx, outer.as("127.0.0.1", path), (&watch{}).guard, 100)
		if err == nil || !strings.HasSuffix(err.Error(), "a redirect carried credentials") || status != 0 || len(body) != 0 {
			t.Errorf("Get(%s) = %d, %q, %v; want the redirect refused", path, status, body, err)
		}
		if err != nil && (strings.Contains(err.Error(), "pass") || strings.Contains(err.Error(), "user")) {
			t.Errorf("the error shows the credentials: %v", err)
		}
		if inner.hits.Load() != 0 {
			t.Errorf("Get(%s): the redirect was followed", path)
		}
	}

	// A redirect cannot leave http and https.
	if status, body, err := o.Get(ctx, outer.as("127.0.0.1", "/to-file"), (&watch{}).guard, 100); err == nil {
		t.Errorf("a redirect to a file address = %d, %q", status, body)
	}
}

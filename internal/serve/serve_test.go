package serve

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gallowforge/holdfast/internal/api"
	"github.com/gallowforge/holdfast/internal/engine"
	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/sim"
	"github.com/gallowforge/holdfast/internal/store"
)

var start = time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC)

const (
	index  = "<!doctype html><title>Holdfast</title><script type=\"module\" src=\"/assets/app-abc123.js\"></script>"
	script = "console.log('holdfast')"
	icon   = "<svg xmlns=\"http://www.w3.org/2000/svg\"/>"
)

// built is an interface as a build leaves it.
func built() fs.FS {
	return fstest.MapFS{
		"index.html":           {Data: []byte(index)},
		"assets/app-abc123.js": {Data: []byte(script)},
		"favicon.svg":          {Data: []byte(icon)},
	}
}

// site is a handler on a pretend machine with one file and one open port,
// and the store behind it.
type site struct {
	t     testing.TB
	h     http.Handler
	store *store.Memory
	api   *api.API
}

func newSite(t testing.TB, files fs.FS, open bool) *site {
	t.Helper()
	w := sim.New(func() time.Time { return start })
	w.Write("go.mod", "module example.com/atlas\n\ngo 1.24\n", start.Add(-time.Hour))
	w.Ports["localhost:8080"] = true
	rules, err := policy.Parse("files .\nnet loopback\n")
	if err != nil {
		t.Fatal(err)
	}
	s := store.NewMemory(rules)
	a := &api.API{
		Engine:  &engine.Engine{Store: s, World: w},
		Version: "1.2.3-test",
		Who:     "alex",
		Where: func() api.Where {
			return api.Where{Project: "atlas", Policy: "files .\nnet loopback\n", Approved: true}
		},
	}
	return &site{t: t, h: Handler(a, files, open), store: s, api: a}
}

// ask is one request; the zero value of every field but the path is what the
// Holdfast page itself would send.
type ask struct {
	method string
	path   string
	body   string
	// host is the Host header; "localhost:8484" when empty.
	host string
	// origin is the Origin header; none when empty.
	origin string
	// bare leaves out the header that marks a call as the page's own.
	bare bool
}

func (s *site) do(a ask) *httptest.ResponseRecorder {
	s.t.Helper()
	if a.method == "" {
		a.method = "GET"
	}
	if a.host == "" {
		a.host = "localhost:8484"
	}
	var body io.Reader
	if a.body != "" {
		body = strings.NewReader(a.body)
	}
	r := httptest.NewRequest(a.method, "http://placeholder"+a.path, body)
	r.Host = a.host
	if a.host == "none" {
		r.Host = ""
	}
	if a.origin != "" {
		r.Header.Set("Origin", a.origin)
	}
	if a.method == "POST" {
		r.Header.Set("Content-Type", "application/json")
		if !a.bare {
			r.Header.Set(Header, "1")
		}
	}
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	return w
}

// call makes an API call the way the page does.
func (s *site) call(method, body string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.do(ask{method: "POST", path: "/api/" + method, body: body})
}

// envelope reads the JSON an API call was answered with.
func envelope(t testing.TB, w *httptest.ResponseRecorder) (reply struct {
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result"`
	Error   string          `json:"error"`
	Refused bool            `json:"refused"`
}) {
	t.Helper()
	if got := w.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", got)
	}
	dec := json.NewDecoder(w.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&reply); err != nil {
		t.Fatalf("the answer is not the envelope the page reads: %v", err)
	}
	if dec.More() {
		t.Error("there is something after the envelope")
	}
	return reply
}

const remember = `{"claim":"The API listens on port 8080","check":"port localhost:8080"}`

// stored is how many facts the store holds.
func (s *site) stored() int {
	s.t.Helper()
	all, err := s.store.Facts()
	if err != nil {
		s.t.Fatal(err)
	}
	return len(all)
}

// --- the API over HTTP ---

func TestACallFromThePageIsAnswered(t *testing.T) {
	s := newSite(t, built(), false)
	w := s.call("remember", remember)
	if w.Code != 200 {
		t.Fatalf("remember answered %d: %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: an answer is true only for the moment", got)
	}
	reply := envelope(t, w)
	var got struct {
		Fact    engine.View `json:"fact"`
		Already bool        `json:"already"`
	}
	if err := json.Unmarshal(reply.Result, &got); err != nil || !reply.OK {
		t.Fatalf("the answer is ok=%v with %s (%v)", reply.OK, reply.Result, err)
	}
	if got.Fact.Status != fact.Holds || got.Fact.By != "alex" || got.Already {
		t.Errorf("stored %+v, already %v; want a new fact that holds, by alex", got.Fact, got.Already)
	}
	if s.stored() != 1 {
		t.Errorf("the store holds %d facts, want the one", s.stored())
	}

	w = s.call("state", "")
	var state struct {
		Version string        `json:"version"`
		Project string        `json:"project"`
		Facts   []engine.View `json:"facts"`
	}
	if reply := envelope(t, w); w.Code != 200 || json.Unmarshal(reply.Result, &state) != nil {
		t.Fatalf("state answered %d", w.Code)
	}
	if state.Version != "1.2.3-test" || state.Project != "atlas" || len(state.Facts) != 1 {
		t.Errorf("state says version %q, project %q, %d facts", state.Version, state.Project, len(state.Facts))
	}
}

// broken is a store whose disk has failed.
type broken struct {
	store.Store
}

func (broken) Facts() ([]*fact.Fact, error) { return nil, errors.New("the disk is full") }

func TestTheStatusSaysHowACallWent(t *testing.T) {
	s := newSite(t, built(), false)
	for _, tc := range []struct {
		name, method, body string
		status             int
		refused            bool
		message            string
	}{
		{"done", "recall", `{"query":"port"}`, 200, false, ""},
		{"no body at all", "verify", "", 200, false, ""},
		{"a call that does not exist", "nope", "{}", 404, false, "there is no such call"},
		{"a mistake of the caller's", "retract", `{"id":"f-zzzzzz","reason":"wrong"}`, 422, true, `there is no fact with the id "f-zzzzzz"`},
		{"arguments that are not right", "recall", `{"nope":1}`, 422, true, `the request is not right: unknown field "nope"`},
		{"a body that is not JSON", "recall", `port`, 422, true, "the request is not right: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := s.call(tc.method, tc.body)
			reply := envelope(t, w)
			if w.Code != tc.status || reply.OK != (tc.status == 200) || reply.Refused != tc.refused {
				t.Errorf("answered %d, ok %v, refused %v; want %d", w.Code, reply.OK, reply.Refused, tc.status)
			}
			if !strings.HasPrefix(reply.Error, tc.message) || (tc.message == "") != (reply.Error == "") {
				t.Errorf("the error is %q, want it to begin %q", reply.Error, tc.message)
			}
			if got := w.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}

	t.Run("a fault inside Holdfast", func(t *testing.T) {
		s := newSite(t, built(), false)
		s.api.Engine.Store = broken{s.store}
		w := s.call("state", "")
		reply := envelope(t, w)
		if w.Code != 500 || reply.OK || reply.Refused || reply.Error != "Holdfast could not do that: the disk is full" {
			t.Errorf("answered %d %q (refused %v), want 500 and the fault", w.Code, reply.Error, reply.Refused)
		}
	})
}

// The page can retract and vouch, so a call must prove it comes from the
// page: it carries a header that a page on another site cannot set.
func TestACallWithoutTheHeaderIsRefused(t *testing.T) {
	if Header != "X-Holdfast" {
		t.Fatalf("the header is %q; the page sends X-Holdfast", Header)
	}
	s := newSite(t, built(), false)
	for _, method := range []string{"remember", "state", "retract", "nope"} {
		w := s.do(ask{method: "POST", path: "/api/" + method, body: remember, bare: true})
		reply := envelope(t, w)
		if w.Code != 403 || reply.OK || reply.Error != "this call did not come from the Holdfast page" {
			t.Errorf("%s without the header answered %d %q, want 403", method, w.Code, reply.Error)
		}
	}
	// A form on another site can post, but it cannot add the header.
	r := httptest.NewRequest("POST", "http://localhost:8484/api/remember", strings.NewReader("claim=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Errorf("a form post answered %d, want 403", w.Code)
	}
	// An empty header is no header.
	r = httptest.NewRequest("POST", "http://localhost:8484/api/remember", strings.NewReader(remember))
	r.Header.Set(Header, "")
	w = httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Errorf("a call with an empty header answered %d, want 403", w.Code)
	}
	if s.stored() != 0 {
		t.Errorf("a refused call stored %d facts", s.stored())
	}

	// Any value will do: the page sends "1".
	for _, value := range []string{"1", "yes", "0"} {
		r := httptest.NewRequest("POST", "http://localhost:8484/api/state", nil)
		r.Header.Set(Header, value)
		w := httptest.NewRecorder()
		s.h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Errorf("a call with the header set to %q answered %d", value, w.Code)
		}
	}
}

func TestACallFromAnotherSiteIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, host, origin string
		ok                 bool
	}{
		{"no origin: a tool, not a browser", "localhost:8484", "", true},
		{"the page itself", "localhost:8484", "http://localhost:8484", true},
		{"the page by address", "127.0.0.1:8484", "http://127.0.0.1:8484", true},
		{"the page over IPv6", "[::1]:8484", "http://[::1]:8484", true},
		{"another site", "localhost:8484", "https://evil.example", false},
		{"another site that names this machine in its path", "localhost:8484", "https://evil.example/localhost:8484", false},
		{"another program on this machine", "localhost:8484", "http://localhost:3000", false},
		{"this machine by another name", "localhost:8484", "http://127.0.0.1:8484", false},
		{"a page with no origin of its own", "localhost:8484", "null", false},
		{"an origin that is not a URL", "localhost:8484", "%zz", false},
		{"an origin with a name in front", "localhost:8484", "http://localhost:8484.evil.example", false},
		{"an origin with this machine as a user name", "localhost:8484", "http://localhost:8484@evil.example", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSite(t, built(), false)
			w := s.do(ask{method: "POST", path: "/api/remember", body: remember, host: tc.host, origin: tc.origin})
			reply := envelope(t, w)
			if tc.ok {
				if w.Code != 200 || s.stored() != 1 {
					t.Errorf("answered %d %q and stored %d facts, want it done", w.Code, reply.Error, s.stored())
				}
				return
			}
			if w.Code != 403 || reply.Error != "this call did not come from the Holdfast page" {
				t.Errorf("answered %d %q, want 403", w.Code, reply.Error)
			}
			if s.stored() != 0 {
				t.Errorf("a call from %s stored a fact", tc.origin)
			}
		})
	}
}

// A page on another site can point a name of its own at 127.0.0.1 and so
// reach this server; it cannot make the browser say "localhost" in the Host
// header. So only requests addressed to a local name are answered.
func TestOnlyRequestsAddressedToThisMachineAreAnswered(t *testing.T) {
	for _, tc := range []struct {
		host  string
		local bool
	}{
		{"localhost", true},
		{"localhost:8484", true},
		{"LOCALHOST:8484", true},
		{"127.0.0.1", true},
		{"127.0.0.1:8484", true},
		{"127.4.5.6:8484", true},
		{"[::1]:8484", true},
		{"[::1]", true},
		{"holdfast.localhost:8484", true},
		{"evil.example", false},
		{"evil.example:8484", false},
		{"localhost.evil.example:8484", false},
		{"127.0.0.1.evil.example:8484", false},
		{"notlocalhost:8484", false},
		{"192.168.1.20:8484", false},
		{"10.0.0.5", false},
		{"0.0.0.0:8484", false},
		{"[::]:8484", false},
		{"[2001:db8::1]:8484", false},
		{"workstation:8484", false},
		{"none", false},
	} {
		t.Run(tc.host, func(t *testing.T) {
			closed := newSite(t, built(), false)
			page := closed.do(ask{path: "/", host: tc.host})
			call := closed.do(ask{method: "POST", path: "/api/remember", body: remember, host: tc.host})
			if tc.local {
				if page.Code != 200 || call.Code != 200 {
					t.Errorf("a request to %s answered %d for the page and %d for a call, want both served", tc.host, page.Code, call.Code)
				}
			} else {
				for name, w := range map[string]*httptest.ResponseRecorder{"the page": page, "a call": call} {
					if w.Code != 403 || !strings.Contains(w.Body.String(), "holdfast answers on this machine only") {
						t.Errorf("%s asked of %s answered %d %q, want 403", name, tc.host, w.Code, w.Body)
					}
					if strings.Contains(w.Body.String(), "Holdfast</title>") {
						t.Errorf("%s asked of %s was served the interface", name, tc.host)
					}
				}
				if closed.stored() != 0 {
					t.Errorf("a call addressed to %s stored a fact", tc.host)
				}
			}

			// Opened up by the person who started it, any name is answered.
			open := newSite(t, built(), true)
			if w := open.do(ask{path: "/", host: tc.host}); w.Code != 200 {
				t.Errorf("an open server answered %d for the page asked of %s", w.Code, tc.host)
			}
			if w := open.do(ask{method: "POST", path: "/api/remember", body: remember, host: tc.host}); w.Code != 200 || open.stored() != 1 {
				t.Errorf("an open server answered %d for a call asked of %s", w.Code, tc.host)
			}
		})
	}
}

// Opening the server to the network does not open it to other sites.
func TestAnOpenServerStillChecksWhoIsCalling(t *testing.T) {
	s := newSite(t, built(), true)
	if w := s.do(ask{method: "POST", path: "/api/remember", body: remember, host: "192.168.1.20:8484", bare: true}); w.Code != 403 {
		t.Errorf("a call without the header answered %d on an open server", w.Code)
	}
	if w := s.do(ask{method: "POST", path: "/api/remember", body: remember, host: "192.168.1.20:8484", origin: "https://evil.example"}); w.Code != 403 {
		t.Errorf("a call from another site answered %d on an open server", w.Code)
	}
	if s.stored() != 0 {
		t.Errorf("a refused call stored %d facts", s.stored())
	}
	if w := s.do(ask{method: "POST", path: "/api/remember", body: remember, host: "192.168.1.20:8484", origin: "http://192.168.1.20:8484"}); w.Code != 200 || s.stored() != 1 {
		t.Errorf("the page itself, reached over the network, answered %d", w.Code)
	}
}

func TestOnlyAPostReachesTheAPI(t *testing.T) {
	s := newSite(t, built(), false)
	s.call("remember", remember)
	id := ""
	if all, _ := s.store.Facts(); len(all) == 1 {
		id = all[0].ID
	}
	body := `{"id":"` + id + `","reason":"through the back door"}`

	for _, method := range []string{"GET", "HEAD", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		for _, path := range []string{"/api/retract", "/api/state", "/api/retract?id=" + id + "&reason=x"} {
			r := httptest.NewRequest(method, "http://localhost:8484"+path, strings.NewReader(body))
			r.Header.Set(Header, "1")
			w := httptest.NewRecorder()
			s.h.ServeHTTP(w, r)
			if w.Code != 404 && w.Code != 405 {
				t.Errorf("%s %s answered %d, want 404 or 405", method, path, w.Code)
			}
			if strings.Contains(w.Body.String(), `"ok"`) || strings.Contains(w.Body.String(), "1.2.3-test") {
				t.Errorf("%s %s was answered by the API: %s", method, path, w.Body)
			}
			// A browser asking leave to send the header is never told yes.
			for name := range w.Header() {
				if strings.HasPrefix(name, "Access-Control-") {
					t.Errorf("%s %s answered with %s: no other site is ever given leave", method, path, name)
				}
			}
		}
	}
	if kept, err := s.store.Get(id); err != nil || !kept.Live() {
		t.Errorf("the fact was withdrawn by something other than a POST: %+v, %v", kept, err)
	}

	// And a POST reaches nothing but the API.
	for _, path := range []string{"/", "/index.html", "/assets/app-abc123.js", "/api", "/api/", "/api/state/extra", "/api/..", "/api/../api/state/.."} {
		w := s.do(ask{method: "POST", path: path, body: "{}"})
		if w.Code == 200 {
			t.Errorf("POST %s answered 200: %s", path, w.Body)
		}
	}
}

func TestABodyOverTheLimitIsTurnedDownUnread(t *testing.T) {
	s := newSite(t, built(), false)
	// A request of exactly the limit is still read: it is valid JSON, padded.
	pad := func(size int) string {
		open, shut := `{"claim":"The API listens on port 8080","check":"port localhost:8080"`, `}`
		return open + strings.Repeat(" ", size-len(open)-len(shut)) + shut
	}
	if w := s.call("remember", pad(maxBody)); w.Code != 200 || s.stored() != 1 {
		t.Fatalf("a body of exactly %d bytes answered %d: %s", maxBody, w.Code, w.Body)
	}

	s = newSite(t, built(), false)
	w := s.call("remember", pad(maxBody+1))
	reply := envelope(t, w)
	if w.Code != 413 || reply.OK || reply.Error != "the request is too large" {
		t.Errorf("a body one byte over answered %d %q, want 413", w.Code, reply.Error)
	}
	w = s.call("remember", pad(4<<20))
	if w.Code != 413 {
		t.Errorf("a body of 4 MiB answered %d, want 413", w.Code)
	}
	if s.stored() != 0 {
		t.Errorf("a body that was too large stored %d facts", s.stored())
	}
	// The server goes on answering.
	if w := s.call("state", ""); w.Code != 200 {
		t.Errorf("after that, state answered %d", w.Code)
	}
}

// --- headers ---

var security = map[string]string{
	"X-Content-Type-Options":       "nosniff",
	"X-Frame-Options":              "DENY",
	"Referrer-Policy":              "no-referrer",
	"Cross-Origin-Resource-Policy": "same-origin",
	"Content-Security-Policy":      "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
}

func TestEveryAnswerCarriesTheSecurityHeaders(t *testing.T) {
	s := newSite(t, built(), false)
	for _, a := range []ask{
		{path: "/"},
		{path: "/assets/app-abc123.js"},
		{path: "/favicon.svg"},
		{path: "/no/such/file"},
		{method: "HEAD", path: "/"},
		{method: "POST", path: "/api/state"},
		{method: "POST", path: "/api/nope"},
		{method: "POST", path: "/api/retract", body: `{"id":"f-zzzzzz","reason":"x"}`},
		{method: "POST", path: "/api/state", bare: true},
		{method: "POST", path: "/api/state", origin: "https://evil.example"},
		{method: "POST", path: "/api/remember", body: strings.Repeat("x", maxBody+1)},
		{method: "DELETE", path: "/api/state"},
		{method: "POST", path: "/"},
		// refused for the name it was asked under, and still not framed or sniffed
		{path: "/", host: "attacker.example"},
		{method: "POST", path: "/api/state", host: "attacker.example"},
	} {
		w := s.do(a)
		for name, want := range security {
			if got := w.Header().Get(name); got != want {
				t.Errorf("%s %s (status %d): %s = %q, want %q", a.method, a.path, w.Code, name, got, want)
			}
		}
	}
	// The same goes for a binary without its interface, and an open server.
	for _, s := range []*site{newSite(t, fstest.MapFS{}, false), newSite(t, built(), true)} {
		w := s.do(ask{path: "/", host: "localhost:8484"})
		for name, want := range security {
			if got := w.Header().Get(name); got != want {
				t.Errorf("status %d: %s = %q, want %q", w.Code, name, got, want)
			}
		}
	}
}

func TestTheContentPolicyLetsNothingInFromOutside(t *testing.T) {
	policy := security["Content-Security-Policy"]
	rules := map[string]string{}
	for _, rule := range strings.Split(policy, ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(rule), " ")
		rules[name] = value
	}
	if rules["default-src"] != "'self'" {
		t.Errorf("default-src is %q, want only this server", rules["default-src"])
	}
	if rules["frame-ancestors"] != "'none'" || rules["base-uri"] != "'none'" || rules["form-action"] != "'none'" {
		t.Errorf("frame-ancestors %q, base-uri %q, form-action %q; want none of each", rules["frame-ancestors"], rules["base-uri"], rules["form-action"])
	}
	// Scripts fall under default-src: none inline, none evaluated, none from elsewhere.
	if script, own := rules["script-src"]; own {
		t.Errorf("script-src is set to %q; scripts should stay under default-src 'self'", script)
	}
	for _, word := range []string{"unsafe-eval", "*", "http:", "https:"} {
		if strings.Contains(policy, word) {
			t.Errorf("the policy contains %q: %s", word, policy)
		}
	}
	if strings.Count(policy, "'unsafe-inline'") != 1 || !strings.Contains(rules["style-src"], "'unsafe-inline'") {
		t.Errorf("inline content is allowed for more than styles: %s", policy)
	}
}

// --- the interface ---

func TestTheInterfaceIsServedFromTheFiles(t *testing.T) {
	s := newSite(t, built(), false)
	for _, tc := range []struct {
		path, body, kind, cache string
	}{
		{"/", index, "text/html", "no-cache"},
		{"/assets/app-abc123.js", script, "javascript", "public, max-age=31536000, immutable"},
		{"/favicon.svg", icon, "image/svg+xml", "no-cache"},
	} {
		w := s.do(ask{path: tc.path})
		if w.Code != 200 || w.Body.String() != tc.body {
			t.Errorf("GET %s answered %d %q, want the file", tc.path, w.Code, w.Body)
		}
		if got := w.Header().Get("Content-Type"); !strings.Contains(got, tc.kind) {
			t.Errorf("GET %s: Content-Type = %q, want %s", tc.path, got, tc.kind)
		}
		// The page itself is always asked for again, so a new binary shows its
		// new interface; the files it names carry their content in their name.
		if got := w.Header().Get("Cache-Control"); got != tc.cache {
			t.Errorf("GET %s: Cache-Control = %q, want %q", tc.path, got, tc.cache)
		}

		head := s.do(ask{method: "HEAD", path: tc.path})
		if head.Code != 200 || head.Body.Len() != 0 {
			t.Errorf("HEAD %s answered %d with %d bytes of body", tc.path, head.Code, head.Body.Len())
		}
	}

	for _, path := range []string{"/nope", "/assets/nope.js", "/index.htm", "/api/state", "/../go.mod", "/..%2f..%2fetc/passwd", "/assets/../../secret"} {
		w := s.do(ask{path: path})
		if w.Code == 200 {
			t.Errorf("GET %s answered 200 with %q", path, w.Body)
		}
		if strings.Contains(w.Body.String(), "module example.com") {
			t.Errorf("GET %s reached a file outside the interface", path)
		}
	}
}

func TestABinaryWithoutItsInterfaceSaysSoAndStillAnswersCalls(t *testing.T) {
	for name, files := range map[string]fs.FS{
		"no files at all":     fstest.MapFS{},
		"only a placeholder":  fstest.MapFS{".gitkeep": {}},
		"assets but no index": fstest.MapFS{"assets/app-abc123.js": {Data: []byte(script)}},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSite(t, files, false)
			for _, path := range []string{"/", "/index.html", "/assets/app-abc123.js", "/anything"} {
				w := s.do(ask{path: path})
				want := "This holdfast binary was built without its web interface. Build it with \"make build\".\n"
				if w.Code != 404 || w.Body.String() != want {
					t.Errorf("GET %s answered %d %q, want 404 and how to build the interface", path, w.Code, w.Body)
				}
				if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
					t.Errorf("GET %s: Content-Type = %q", path, got)
				}
			}
			if w := s.call("remember", remember); w.Code != 200 || s.stored() != 1 {
				t.Errorf("a call answered %d on a binary without its interface", w.Code)
			}
		})
	}
}

// --- where it listens ---

func TestLoopbackSaysWhetherAnAddressStaysOnThisMachine(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8484", true},
		{"127.0.0.1:0", true},
		{"127.8.9.10:8484", true},
		{"[::1]:8484", true},
		{"localhost:8484", true},
		{"LOCALHOST:8484", true},
		{"0.0.0.0:8484", false},
		{"[::]:8484", false},
		{":8484", false},
		{"192.168.1.20:8484", false},
		{"10.0.0.5:80", false},
		{"[2001:db8::1]:8484", false},
		{"[fe80::1%eth0]:8484", false},
		{"example.com:8484", false},
		{"localhost.example.com:8484", false},
		{"workstation:8484", false},
		// Not an address to listen on at all.
		{"", false},
		{"127.0.0.1", false},
		{"localhost", false},
		{"::1", false},
		{"8484", false},
	} {
		if got := Loopback(tc.addr); got != tc.want {
			t.Errorf("Loopback(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestServerHasLimitsAndServesOverARealConnection(t *testing.T) {
	s := newSite(t, built(), false)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	srv := Server(addr, s.h)
	if srv.Addr != addr || srv.Handler == nil {
		t.Errorf("the server is for %q with handler %v", srv.Addr, srv.Handler)
	}
	// A client that sends nothing, or never stops sending, is not waited for
	// for ever; proving every fact is given the time it may take.
	if srv.ReadHeaderTimeout <= 0 || srv.ReadHeaderTimeout > 30*time.Second {
		t.Errorf("ReadHeaderTimeout = %s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout <= 0 || srv.ReadTimeout > time.Minute {
		t.Errorf("ReadTimeout = %s", srv.ReadTimeout)
	}
	if srv.WriteTimeout < engine.VerifyBudget {
		t.Errorf("WriteTimeout = %s, shorter than a full proof may take (%s)", srv.WriteTimeout, engine.VerifyBudget)
	}
	if srv.IdleTimeout <= 0 || srv.MaxHeaderBytes <= 0 || srv.MaxHeaderBytes > 1<<20 {
		t.Errorf("IdleTimeout = %s, MaxHeaderBytes = %d", srv.IdleTimeout, srv.MaxHeaderBytes)
	}

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	t.Cleanup(func() {
		srv.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("the server stopped with %v", err)
		}
	})
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(page) != index {
		t.Errorf("GET / answered %d %q", resp.StatusCode, page)
	}
	if got := resp.Header.Get("Content-Security-Policy"); got != security["Content-Security-Policy"] {
		t.Errorf("Content-Security-Policy = %q", got)
	}

	post := func(header bool, origin string) (int, []byte) {
		req, err := http.NewRequest("POST", "http://"+addr+"/api/remember", bytes.NewReader([]byte(remember)))
		if err != nil {
			t.Fatal(err)
		}
		if header {
			req.Header.Set(Header, "1")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	if status, body := post(false, ""); status != 403 {
		t.Errorf("a call without the header answered %d %s", status, body)
	}
	if status, body := post(true, "https://evil.example"); status != 403 {
		t.Errorf("a call from another site answered %d %s", status, body)
	}
	if s.stored() != 0 {
		t.Fatalf("a refused call stored %d facts", s.stored())
	}
	if status, body := post(true, "http://"+addr); status != 200 || !bytes.Contains(body, []byte(`"ok":true`)) || s.stored() != 1 {
		t.Errorf("the page's own call answered %d %s", status, body)
	}
}

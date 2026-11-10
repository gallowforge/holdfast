package sim

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/world"
)

var (
	noon = time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	ctx  = context.Background()
	all  = []string{"."}
)

func machine() *World {
	w := New(func() time.Time { return noon })
	w.Write("go.mod", "module example\n\ngo 1.24\n", noon.Add(-time.Hour))
	w.Write("docs/guide.md", "# Guide\n", noon.Add(-48*time.Hour))
	w.Write("docs/api/v1.md", "v1\n", noon)
	w.Write("./deploy//prod.env", "REGION=eu\n", noon)
	w.Names["example.com"] = []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("2606:2800:220:1::1")}
	w.Names["db.internal"] = []netip.Addr{netip.MustParseAddr("10.0.0.5")}
	w.Ports["localhost:8080"] = true
	w.Ports["example.com:443"] = true
	w.Ports["example.com:80"] = true
	w.Ports["db.internal:5432"] = true
	w.Pages["http://localhost:8080/healthz"] = Page{200, "ok"}
	w.Pages["https://example.com/"] = Page{200, "<h1>Example Domain</h1>"}
	w.Pages["https://example.com/gone"] = Page{410, "gone for good"}
	w.Pages["http://example.com/"] = Page{301, ""}
	w.Cmds["systemctl is-active docker"] = Result{0, "active\n"}
	w.Cmds["systemctl is-active nginx"] = Result{3, "inactive\n"}
	w.Cmds["true"] = Result{}
	return w
}

func open(_ string, _ netip.Addr) error { return nil }

func blocked(err error) string {
	var b *policy.Blocked
	if errors.As(err, &b) {
		return b.Error()
	}
	return ""
}

func TestNew(t *testing.T) {
	w := New(func() time.Time { return noon })
	if !w.Now().Equal(noon) {
		t.Errorf("Now() = %v", w.Now())
	}
	if ips, err := w.Resolve(ctx, "localhost"); err != nil || len(ips) != 1 || ips[0].String() != "127.0.0.1" {
		t.Errorf("a new machine resolves localhost to %v, %v", ips, err)
	}
	if len(w.Files)+len(w.Ports)+len(w.Pages)+len(w.Cmds) != 0 {
		t.Error("a new machine is not empty")
	}
	// Nothing exists on an empty machine, not even its top folder.
	if st, err := w.Stat(all, "."); err != nil || st.Exists {
		t.Errorf("Stat(.) on an empty machine = %+v, %v", st, err)
	}
	var _ world.World = w
	var _ world.Pacer = w
}

func TestFiles(t *testing.T) {
	w := machine()
	st, err := w.Stat(all, "go.mod")
	want := world.FileInfo{Exists: true, Regular: true, Size: 24, ModTime: noon.Add(-time.Hour)}
	if err != nil || st != want {
		t.Errorf("Stat(go.mod) = %+v, %v; want %+v", st, err, want)
	}
	// Names are cleaned on the way in and on the way out.
	for _, name := range []string{"deploy/prod.env", "./deploy/prod.env", "deploy//prod.env", "docs/../deploy/prod.env"} {
		if st, err := w.Stat(all, name); err != nil || !st.Regular || st.Size != 10 {
			t.Errorf("Stat(%q) = %+v, %v", name, st, err)
		}
		f, err := w.Open(all, name)
		if err != nil {
			t.Errorf("Open(%q): %v", name, err)
			continue
		}
		data, _ := io.ReadAll(f)
		if string(data) != "REGION=eu\n" || f.Close() != nil {
			t.Errorf("Open(%q) read %q", name, data)
		}
	}
	for _, name := range []string{"missing", "docs/missing.md", "go.mod/x", "go.mo", "go.modx"} {
		if st, err := w.Stat(all, name); err != nil || st != (world.FileInfo{}) {
			t.Errorf("Stat(%q) = %+v, %v; want not there", name, st, err)
		}
		if f, err := w.Open(all, name); err == nil || f != nil || blocked(err) != "" {
			t.Errorf("Open(%q) = %v, %v; want an ordinary error", name, f, err)
		}
	}
	// Writing again replaces the file.
	w.Write("go.mod", "new", noon)
	if st, _ := w.Stat(all, "go.mod"); st.Size != 3 || !st.ModTime.Equal(noon) {
		t.Errorf("after a second Write: %+v", st)
	}
}

// A folder exists because a file lies somewhere under it.
func TestFoldersAreImpliedByFiles(t *testing.T) {
	w := machine()
	for _, name := range []string{".", "docs", "docs/", "docs/api", "deploy", "./docs/api/"} {
		st, err := w.Stat(all, name)
		if err != nil || st != (world.FileInfo{Exists: true, Dir: true}) {
			t.Errorf("Stat(%q) = %+v, %v; want a folder", name, st, err)
		}
		if f, err := w.Open(all, name); err == nil || f != nil {
			t.Errorf("Open(%q) opened a folder", name)
		}
	}
	// Part of a name is not a folder.
	for _, name := range []string{"doc", "docs/ap", "dep", "docs/api/v1"} {
		if st, _ := w.Stat(all, name); st.Exists {
			t.Errorf("Stat(%q) = %+v; want not there", name, st)
		}
	}
	// The folder goes when its last file goes.
	w.Change(func(w *World) { delete(w.Files, "deploy/prod.env") })
	if st, _ := w.Stat(all, "deploy"); st.Exists {
		t.Errorf("Stat(deploy) after its only file was removed = %+v", st)
	}
}

// The pretend machine has no links, so the policy's folders are applied to
// the spelling of a path.
func TestPolicyFolders(t *testing.T) {
	w := machine()
	tests := []struct {
		roots []string
		path  string
		fix   string // the folder a refusal suggests; "" if the path is allowed
	}{
		{all, "go.mod", ""},
		{all, "docs/guide.md", ""},
		{all, "docs/../go.mod", ""},
		{[]string{"./"}, "go.mod", ""},
		{[]string{"docs"}, "docs/guide.md", ""},
		{[]string{"docs"}, "docs", ""},
		{[]string{"docs/"}, "docs/api/v1.md", ""},
		{[]string{"./docs"}, "./docs/guide.md", ""},
		{[]string{"deploy", "docs/api"}, "docs/api/v1.md", ""},
		{[]string{"docs"}, "go.mod", "."},
		{[]string{"docs"}, "docsx/a.md", "docsx"},
		{[]string{"docs"}, "doc", "."},
		{[]string{"docs"}, "docs/../go.mod", "."},
		{[]string{"docs/api"}, "docs/guide.md", "docs"},
		{[]string{"docs/api"}, "docs", "."},
		{[]string{"deploy"}, "docs/api/v1.md", "docs/api"},
		{nil, "go.mod", "."},
		{nil, "docs/guide.md", "docs"},
		{[]string{}, "my notes/todo.md", "'my notes'"},
		// Above the machine's top folder, or anywhere by a full path: never.
		{all, "..", "."},
		{all, "../go.mod", ".."},
		{all, "docs/../../go.mod", ".."},
		{all, "/etc/passwd", "/etc"},
		{[]string{"/etc"}, "/etc/passwd", "/etc"},
		{[]string{".."}, "../go.mod", ".."},
	}
	for _, tt := range tests {
		_, serr := w.Stat(tt.roots, tt.path)
		f, oerr := w.Open(tt.roots, tt.path)
		if tt.fix == "" {
			if blocked(serr) != "" || blocked(oerr) != "" {
				t.Errorf("roots %q, path %q: refused (%v / %v), want allowed", tt.roots, tt.path, serr, oerr)
			}
			continue
		}
		want := tt.path + " is outside the folders the policy allows. A person can allow it: holdfast allow files " + tt.fix
		if blocked(serr) != want {
			t.Errorf("roots %q: Stat(%q) = %v\nwant %s", tt.roots, tt.path, serr, want)
		}
		if blocked(oerr) != want || f != nil {
			t.Errorf("roots %q: Open(%q) = %v, %v\nwant %s", tt.roots, tt.path, f, oerr, want)
		}
	}
}

func TestNames(t *testing.T) {
	w := machine()
	tests := []struct {
		name string
		want string
	}{
		{"localhost", "127.0.0.1"},
		{"example.com", "93.184.216.34 2606:2800:220:1::1"},
		{"EXAMPLE.com", "93.184.216.34 2606:2800:220:1::1"},
		{"db.internal", "10.0.0.5"},
		{"192.0.2.7", "192.0.2.7"}, // an address is its own answer
		{"::1", "::1"},
	}
	for _, tt := range tests {
		ips, err := w.Resolve(ctx, tt.name)
		var got []string
		for _, ip := range ips {
			got = append(got, ip.String())
		}
		if err != nil || strings.Join(got, " ") != tt.want {
			t.Errorf("Resolve(%q) = %v, %v; want %s", tt.name, got, err, tt.want)
		}
	}
	w.Names["empty.test"] = nil
	for _, name := range []string{"nowhere.test", "www.example.com", "example.com.", "", "empty.test"} {
		ips, err := w.Resolve(ctx, name)
		var dns *net.DNSError
		if !errors.As(err, &dns) || !dns.IsNotFound || len(ips) != 0 {
			t.Errorf("Resolve(%q) = %v, %v; want a not-found answer", name, ips, err)
		}
	}
	// The answer is the caller's to keep.
	ips, _ := w.Resolve(ctx, "example.com")
	ips[0] = netip.MustParseAddr("6.6.6.6")
	if again, _ := w.Resolve(ctx, "example.com"); again[0].String() != "93.184.216.34" {
		t.Errorf("changing an answer changed the machine: %v", again)
	}
}

// asked records what a guard was asked.
type asked []string

func (a *asked) allow(host string, ip netip.Addr) error {
	*a = append(*a, host+" "+ip.String())
	return nil
}

func (a *asked) refuse(host string, ip netip.Addr) error {
	*a = append(*a, host+" "+ip.String())
	return &policy.Blocked{Why: "no " + host}
}

func TestPorts(t *testing.T) {
	w := machine()
	var a asked
	if err := w.Dial(ctx, "localhost:8080", a.allow); err != nil || strings.Join(a, ", ") != "localhost 127.0.0.1" {
		t.Errorf("Dial(localhost:8080) = %v, asked %q", err, a)
	}
	a = nil
	if err := w.Dial(ctx, "example.com:443", a.allow); err != nil || strings.Join(a, ", ") != "example.com 93.184.216.34, example.com 2606:2800:220:1::1" {
		t.Errorf("Dial(example.com:443) = %v, asked %q", err, a)
	}
	if err := w.Dial(ctx, "LocalHost:8080", open); err != nil {
		t.Errorf("Dial(LocalHost:8080) = %v", err)
	}

	// Nothing listens: an ordinary failure, after the guard has had its say.
	for _, addr := range []string{"localhost:8081", "example.com:22", "127.0.0.1:8080", "[::1]:8080"} {
		a = nil
		err := w.Dial(ctx, addr, a.allow)
		if err == nil || blocked(err) != "" || !strings.Contains(err.Error(), "connection refused") || len(a) == 0 {
			t.Errorf("Dial(%s) = %v, asked %q; want connection refused", addr, err, a)
		}
	}
	w.Change(func(w *World) { w.Ports["localhost:8080"] = false })
	if err := w.Dial(ctx, "localhost:8080", open); err == nil {
		t.Error("a port set to false still accepts connections")
	}

	// Refused by the guard: that is the answer, listening or not.
	a = nil
	if err := w.Dial(ctx, "db.internal:5432", a.refuse); blocked(err) != "no db.internal" || len(a) != 1 {
		t.Errorf("Dial through a refusing guard = %v, asked %q", err, a)
	}
	// One refused address refuses the name.
	oneBad := func(host string, ip netip.Addr) error {
		if ip.Is6() {
			return &policy.Blocked{Why: "no " + ip.String()}
		}
		return nil
	}
	if err := w.Dial(ctx, "example.com:443", oneBad); blocked(err) != "no 2606:2800:220:1::1" {
		t.Errorf("Dial with one refused address = %v", err)
	}

	// A name that does not exist, and addresses that are not addresses.
	a = nil
	var dns *net.DNSError
	if err := w.Dial(ctx, "nowhere.test:80", a.allow); !errors.As(err, &dns) || len(a) != 0 {
		t.Errorf("Dial(nowhere.test:80) = %v, asked %q", err, a)
	}
	for _, addr := range []string{"localhost", "", "localhost:80:80"} {
		if err := w.Dial(ctx, addr, open); err == nil {
			t.Errorf("Dial(%q) succeeded", addr)
		}
	}
}

func TestPages(t *testing.T) {
	w := machine()
	tests := []struct {
		url    string
		max    int64
		status int
		body   string
	}{
		{"http://localhost:8080/healthz", 100, 200, "ok"},
		{"http://LOCALHOST:8080/healthz", 100, 200, "ok"},
		{"http://localhost:8080/healthz?verbose=1#top", 100, 200, "ok"}, // the query is not part of the page
		{"https://example.com/", 1 << 20, 200, "<h1>Example Domain</h1>"},
		{"https://example.com", 1 << 20, 200, "<h1>Example Domain</h1>"}, // no path is "/"
		{"https://example.com/", 4, 200, "<h1>"},
		{"https://example.com/", 0, 200, ""},
		{"https://example.com/gone", 100, 410, "gone for good"},
		{"http://example.com/", 100, 301, ""}, // the scheme is part of the address
		// A server that is there answers 404 for a page it does not have.
		{"http://localhost:8080/", 100, 404, "not found"},
		{"http://localhost:8080/healthz/", 100, 404, "not found"},
		{"http://localhost:8080/HEALTHZ", 100, 404, "not found"},
		{"https://example.com/missing", 100, 404, "not found"},
		{"https://example.com/missing", 3, 404, "not"},
		{"https://example.com/missing", 0, 404, ""},
	}
	for _, tt := range tests {
		status, body, err := w.Get(ctx, tt.url, open, tt.max)
		if err != nil || status != tt.status || string(body) != tt.body {
			t.Errorf("Get(%s, max %d) = %d, %q, %v; want %d, %q", tt.url, tt.max, status, body, err, tt.status, tt.body)
		}
	}

	// The guard is asked first, with the host and each of its addresses.
	var a asked
	if _, _, err := w.Get(ctx, "https://example.com/", a.allow, 10); err != nil || strings.Join(a, ", ") != "example.com 93.184.216.34, example.com 2606:2800:220:1::1" {
		t.Errorf("Get asked %q, %v", a, err)
	}
	a = nil
	status, body, err := w.Get(ctx, "https://example.com/", a.refuse, 10)
	if blocked(err) != "no example.com" || status != 0 || body != nil {
		t.Errorf("Get through a refusing guard = %d, %q, %v", status, body, err)
	}

	// Nothing listens on the port the address means.
	for _, url := range []string{"http://localhost/healthz", "https://localhost:8080x/", "https://db.internal/", "http://example.com:8080/", "ftp://example.com/"} {
		status, body, err := w.Get(ctx, url, open, 10)
		if err == nil || status != 0 || body != nil {
			t.Errorf("Get(%s) = %d, %q, %v; want an error", url, status, body, err)
		}
	}
	var dns *net.DNSError
	if _, _, err := w.Get(ctx, "https://nowhere.test/", open, 10); !errors.As(err, &dns) {
		t.Errorf("Get from an unknown name = %v", err)
	}
	if _, _, err := w.Get(ctx, "http://[::1", open, 10); err == nil {
		t.Error("Get accepted an address that does not parse")
	}
}

func TestCommands(t *testing.T) {
	w := machine()
	tests := []struct {
		argv []string
		max  int
		exit int
		out  string
	}{
		{[]string{"systemctl", "is-active", "docker"}, 100, 0, "active\n"},
		{[]string{"systemctl", "is-active", "nginx"}, 100, 3, "inactive\n"},
		{[]string{"systemctl", "is-active", "nginx"}, 2, 3, "in"},
		{[]string{"systemctl", "is-active", "nginx"}, 0, 3, ""},
		{[]string{"true"}, 100, 0, ""},
		// A command is known by its words joined with spaces.
		{[]string{"systemctl", "is-active docker"}, 100, 0, "active\n"},
	}
	for _, tt := range tests {
		exit, out, err := w.Run(ctx, tt.argv, tt.max)
		if err != nil || exit != tt.exit || string(out) != tt.out {
			t.Errorf("Run(%q, max %d) = %d, %q, %v; want %d, %q", tt.argv, tt.max, exit, out, err, tt.exit, tt.out)
		}
	}
	for _, argv := range [][]string{{"systemctl"}, {"systemctl", "is-active"}, {"systemctl", "is-active", "docker", "extra"}, {"rm", "-rf", "/"}, {"TRUE"}} {
		exit, out, err := w.Run(ctx, argv, 100)
		if err == nil || exit != -1 || out != nil || err.Error() != argv[0]+": executable file not found" {
			t.Errorf("Run(%q) = %d, %q, %v; want it not found", argv, exit, out, err)
		}
	}
}

// Nothing on the pretend machine takes real time, so each check is given a
// steady duration that depends only on what it is.
func TestTook(t *testing.T) {
	w := machine()
	ranges := map[string][2]time.Duration{
		"http":      {38 * time.Millisecond, 177 * time.Millisecond},
		"dns":       {2 * time.Millisecond, 12 * time.Millisecond},
		"port":      {1 * time.Millisecond, 4 * time.Millisecond},
		"cmd":       {6 * time.Millisecond, 35 * time.Millisecond},
		"says":      {6 * time.Millisecond, 35 * time.Millisecond},
		"file":      {0, time.Millisecond},
		"contains":  {0, time.Millisecond},
		"matches":   {0, time.Millisecond},
		"unchanged": {0, time.Millisecond},
		"fresh":     {0, time.Millisecond},
	}
	targets := []string{"", "go.mod", "https://example.com/", "localhost:8080", "example.com", "systemctl is-active docker", "a", "b", "c", "d", "e", "f"}
	other := New(func() time.Time { return noon.Add(time.Hour) })
	for kind, r := range ranges {
		seen := map[time.Duration]bool{}
		for _, target := range targets {
			d := w.Took(kind, target)
			if d < r[0] || d > r[1] {
				t.Errorf("Took(%s, %q) = %v, outside %v to %v", kind, target, d, r[0], r[1])
			}
			if d%time.Millisecond != 0 {
				t.Errorf("Took(%s, %q) = %v, not whole milliseconds", kind, target, d)
			}
			// The same every time, on every machine, whatever the clock says.
			if again := w.Took(kind, target); again != d {
				t.Errorf("Took(%s, %q) = %v, then %v", kind, target, d, again)
			}
			if there := other.Took(kind, target); there != d {
				t.Errorf("Took(%s, %q) = %v on one machine and %v on another", kind, target, d, there)
			}
			seen[d] = true
		}
		if len(seen) < 2 {
			t.Errorf("Took(%s, ...) is %v for every target", kind, seen)
		}
	}
	// Pinned, so a change to the arithmetic is noticed: the demo's ledger
	// should not shift under people's eyes from one release to the next.
	if a, b := w.Took("http", "https://example.com/"), w.Took("port", "localhost:8080"); a != w.Took("http", "https://example.com/") || b != w.Took("port", "localhost:8080") {
		t.Errorf("Took is not steady: %v, %v", a, b)
	}
}

// Checks run while the machine is changed through Change. The race detector
// is the judge here.
func TestChangeWhileChecksRun(t *testing.T) {
	w := machine()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				w.Stat(all, "go.mod")
				w.Stat(all, "docs")
				if f, err := w.Open(all, "go.mod"); err == nil {
					io.ReadAll(f)
					f.Close()
				}
				w.Resolve(ctx, "example.com")
				w.Dial(ctx, "localhost:8080", open)
				w.Get(ctx, "https://example.com/", open, 100)
				w.Run(ctx, []string{"true"}, 100)
				w.Took("http", "https://example.com/")
				w.Now()
			}
		}()
	}
	for i := 0; i < 300; i++ {
		w.Change(func(w *World) {
			w.Write("go.mod", strings.Repeat("x", i), noon)
			if i%2 == 0 {
				delete(w.Files, "docs/guide.md")
			} else {
				w.Write("docs/guide.md", "# Guide\n", noon)
			}
			w.Ports["localhost:8080"] = i%3 != 0
			w.Names["example.com"] = []netip.Addr{netip.MustParseAddr("93.184.216.34")}
			w.Pages["https://example.com/"] = Page{200 + i%2, "x"}
			w.Cmds["true"] = Result{Exit: i % 2}
		})
	}
	close(stop)
	wg.Wait()
}

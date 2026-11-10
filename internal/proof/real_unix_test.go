//go:build unix

package proof

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/world"
)

// project is a real folder with a few files in it, on the real machine.
func project(t *testing.T) (o *world.OS, parent string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"project/go.mod":      "module example\n\ngo 1.24\n",
		"project/Makefile":    makefile,
		"project/secrets.env": "DEBUG=false\nAPI_KEY=" + fileSecret + "\n",
		"project/docs/a.md":   "# A\n",
		"outside/private.txt": fileSecret + "\n",
		"home/.keep":          "",
	} {
		p := filepath.Join(parent, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", filepath.Join(parent, "home"))
	return &world.OS{Root: filepath.Join(parent, "project"), UserAgent: "holdfast-test/1"}, parent
}

// both runs a check and its "not" form on the real machine and returns the
// evidence of the first. Whatever the answer, it never names the folder the
// project lies in, and never shows a secret.
func both(t *testing.T, o *world.OS, parent string, p *policy.Policy, line string, status fact.Status, detail string) {
	t.Helper()
	for _, not := range []bool{false, true} {
		l, want := line, status
		if not {
			l = "not " + line
			switch status {
			case fact.Holds:
				want = fact.Broken
			case fact.Broken:
				want = fact.Holds
			}
		}
		ev := Run(bg, o, p, parse(t, l))
		if ev.Status != want || ev.Detail != detail {
			t.Errorf("%s\n got %s: %s\nwant %s: %s", l, ev.Status, ev.Detail, want, detail)
		}
		oneLine(t, l, ev)
		if strings.Contains(ev.Detail, parent) {
			t.Errorf("%s: the evidence says where the project is on the disk: %s", l, ev.Detail)
		}
		if ev.Ms < 0 || ev.Ms > 9000 || time.Since(ev.At) > time.Minute || ev.At.Location() != time.UTC {
			t.Errorf("%s: took %d ms at %v", l, ev.Ms, ev.At)
		}
	}
}

func TestRealFiles(t *testing.T) {
	o, parent := project(t)
	p := policy.Default()
	root := o.Root
	write := func(name string, size int64) {
		t.Helper()
		f, err := os.Create(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		// A file with a hole: it has a size and takes no room.
		if err := f.Truncate(size); err != nil {
			t.Skipf("cannot make a %d byte file here: %v", size, err)
		}
	}
	write("scan-edge.bin", MaxScan)
	write("scan-over.bin", MaxScan+1)
	write("hash-over.bin", MaxHash+1)
	if err := os.Symlink(filepath.Join(parent, "outside/private.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "outside"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-50 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "docs/a.md"), old, old); err != nil {
		t.Fatal(err)
	}
	mod := sum("module example\n\ngo 1.24\n")
	other := strings.Repeat("ab", 32)
	const through = " leads outside the folders the policy allows (through a symbolic link)"

	// Opening the pipe would wait for ever. Say so instead of hanging the run.
	stuck := time.AfterFunc(20*time.Second, func() {
		panic("a file check did not come back: something that is not a plain file was opened")
	})
	defer stuck.Stop()
	{
		tests := []struct {
			line   string
			status fact.Status
			detail string
		}{
			{"file go.mod", fact.Holds, "go.mod exists"},
			{"file docs", fact.Holds, "docs exists"},
			{"file pipe", fact.Holds, "pipe exists"},
			{"file missing.txt", fact.Broken, "missing.txt does not exist"},
			{"file docs/missing/deeper.txt", fact.Broken, "docs/missing/deeper.txt does not exist"},
			{`contains go.mod "go 1.24"`, fact.Holds, `go.mod contains "go 1.24" (line 3)`},
			{"contains secrets.env API_KEY", fact.Holds, `secrets.env contains "API_KEY" (line 2)`},
			{"contains go.mod python", fact.Broken, `go.mod does not contain "python"`},
			{`matches Makefile "^release:"`, fact.Holds, `Makefile matches "^release:" (line 4)`},
			{`matches secrets.env "^API_KEY=.+$"`, fact.Holds, `secrets.env matches "^API_KEY=.+$" (line 2)`},
			{`matches Makefile "^ship"`, fact.Broken, `Makefile does not match "^ship"`},
			{"unchanged go.mod " + mod, fact.Holds, "go.mod is unchanged (sha256 " + mod[:8] + "…" + mod[60:] + ")"},
			{"unchanged Makefile " + mod, fact.Broken, "Makefile has changed (sha256 was " + mod[:8] + "…" + mod[60:] + ", is now " + sum(makefile)[:8] + "…" + sum(makefile)[60:] + ")"},
			{"unchanged missing.txt " + mod, fact.Broken, "missing.txt does not exist any more"},
			{"fresh go.mod 1h", fact.Holds, "go.mod was modified 0s ago, within 1h"},
			{"fresh docs/a.md 26h", fact.Broken, "docs/a.md was last modified 2d 2h ago, more than 26h"},
			{"fresh docs/a.md 1w", fact.Holds, "docs/a.md was modified 2d 2h ago, within 1w"},
			{"fresh missing.txt 1h", fact.Broken, "missing.txt does not exist"},

			// Not a file that can be read as one.
			{"contains docs x", fact.Blocked, "docs is a folder, not a file"},
			{"unchanged docs " + other, fact.Blocked, "docs is a folder, not a file"},
			{"contains pipe x", fact.Blocked, "pipe is not a regular file"},
			{"matches pipe x", fact.Blocked, "pipe is not a regular file"},
			{"unchanged pipe " + other, fact.Blocked, "pipe is not a regular file"},
			// Too large.
			{"contains scan-over.bin x", fact.Blocked, "scan-over.bin is larger than 8 MiB; Holdfast does not scan files that large"},
			{"matches scan-over.bin x", fact.Blocked, "scan-over.bin is larger than 8 MiB; Holdfast does not scan files that large"},
			{"contains scan-edge.bin x", fact.Broken, `scan-edge.bin does not contain "x"`},
			{"unchanged hash-over.bin " + other, fact.Blocked, "hash-over.bin is larger than 1 GiB"},
			{"file hash-over.bin", fact.Holds, "hash-over.bin exists"},
			// Out of the project, by spelling or through a link.
			{"file ../outside/private.txt", fact.Blocked, "../outside/private.txt is outside the folders the policy allows. A person can allow it: holdfast allow files ../outside"},
			{"contains ../outside/private.txt hunter2", fact.Blocked, "../outside/private.txt is outside the folders the policy allows. A person can allow it: holdfast allow files ../outside"},
			{"contains /etc/passwd root", fact.Blocked, "/etc/passwd is outside the folders the policy allows. A person can allow it: holdfast allow files /etc"},
			{"unchanged ~/.ssh/id_ed25519 " + other, fact.Blocked, "~/.ssh/id_ed25519 is outside the folders the policy allows. A person can allow it: holdfast allow files ~/.ssh"},
			{"file link.txt", fact.Blocked, "link.txt" + through},
			{"contains link.txt hunter2", fact.Blocked, "link.txt" + through},
			{"matches link.txt hunter2", fact.Blocked, "link.txt" + through},
			{"unchanged link.txt " + other, fact.Blocked, "link.txt" + through},
			{"fresh link.txt 1h", fact.Blocked, "link.txt" + through},
			{"contains linked/private.txt hunter2", fact.Blocked, "linked/private.txt" + through},
		}
		for _, tt := range tests {
			both(t, o, parent, p, tt.line, tt.status, tt.detail)
		}
	}

	// Sealing reads the real file, and refuses what a check would refuse.
	c := parse(t, "unchanged go.mod")
	if err := Seal(bg, o, p, c); err != nil || c.Sum != mod {
		t.Errorf("Seal(unchanged go.mod) = %v, %s", err, c.Sum)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ev := Run(bg, o, p, c); ev.Status != fact.Broken || !strings.HasPrefix(ev.Detail, "go.mod has changed (sha256 was "+mod[:8]) {
		t.Errorf("after an edit: %s: %s", ev.Status, ev.Detail)
	}
	if err := os.Remove(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if ev := Run(bg, o, p, c); ev.Status != fact.Broken || ev.Detail != "go.mod does not exist any more" {
		t.Errorf("after removal: %s: %s", ev.Status, ev.Detail)
	}
	for path, want := range map[string]string{
		"go.mod":                 "go.mod does not exist, so there is nothing to keep unchanged",
		"hash-over.bin":          "hash-over.bin is larger than 1 GiB",
		"link.txt":               "link.txt" + through,
		"pipe":                   "pipe is not a regular file",
		"docs":                   "docs is a folder, not a file",
		"../outside/private.txt": "../outside/private.txt is outside the folders the policy allows. A person can allow it: holdfast allow files ../outside",
	} {
		c := &fact.Check{Kind: "unchanged", Path: path}
		if err := Seal(bg, o, p, c); err == nil || err.Error() != want || c.Sum != "" {
			t.Errorf("Seal(unchanged %s) = %v, %q\nwant %s", path, err, c.Sum, want)
		}
	}
}

// A file the operating system will not let us read: its reason is kept, the
// full path it adds is not.
func TestRealUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	o, parent := project(t)
	p := policy.Default()
	if err := os.Chmod(filepath.Join(o.Root, "secrets.env"), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(o.Root, "docs"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(o.Root, "docs"), 0o755) })
	other := strings.Repeat("ab", 32)
	both(t, o, parent, p, "contains secrets.env API_KEY", fact.Blocked, "cannot read secrets.env: permission denied")
	both(t, o, parent, p, "matches secrets.env API_KEY", fact.Blocked, "cannot read secrets.env: permission denied")
	both(t, o, parent, p, "unchanged secrets.env "+other, fact.Blocked, "cannot read secrets.env: permission denied")
	both(t, o, parent, p, "file secrets.env", fact.Holds, "secrets.env exists")
	both(t, o, parent, p, "file docs/a.md", fact.Blocked, "cannot read docs/a.md: permission denied")
	both(t, o, parent, p, "fresh docs/a.md 1h", fact.Blocked, "cannot read docs/a.md: permission denied")
	both(t, o, parent, p, "contains docs/a.md A", fact.Blocked, "cannot read docs/a.md: permission denied")
}

// quiet is a test web server that does not log the broken requests these
// tests make on purpose.
func quiet(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(h)
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.Start()
	t.Cleanup(s.Close)
	return s
}

// raw is a listener on this machine that answers every connection with the
// same bytes and hangs up.
func raw(t *testing.T, answer string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(10 * time.Second))
				// Take the whole request first, so the client is never cut off
				// mid-write and always sees the same thing.
				var req []byte
				buf := make([]byte, 4096)
				for !strings.Contains(string(req), "\r\n\r\n") {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					req = append(req, buf[:n]...)
				}
				io.WriteString(c, answer)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestRealNetwork(t *testing.T) {
	o, parent := project(t)
	p := policy.Default()
	var agent string
	s := quiet(t, func(w http.ResponseWriter, r *http.Request) {
		agent = r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/healthz":
			fmt.Fprint(w, "ok\nsession="+pageSecret)
		case "/token":
			if r.URL.Query().Get("token") == querySecret {
				fmt.Fprint(w, "welcome")
			} else {
				http.Error(w, "no", http.StatusForbidden)
			}
		case "/big":
			w.Write([]byte(strings.Repeat("x", MaxBody)))
			fmt.Fprint(w, "needle")
		case "/edge":
			w.Write([]byte(strings.Repeat("x", MaxBody-6)))
			fmt.Fprint(w, "needle")
		case "/moved":
			http.Redirect(w, r, "/healthz", http.StatusFound)
		case "/with-password":
			http.Redirect(w, r, "http://alex:"+pageSecret+"@"+r.Host+"/healthz?token="+querySecret, http.StatusFound)
		case "/elsewhere":
			http.Redirect(w, r, "http://192.0.2.1/?token="+querySecret, http.StatusFound)
		case "/metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		case "/bad-location":
			w.Header().Set("Location", "http://alex:"+pageSecret+"@[::1/%zz")
			w.WriteHeader(http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})
	at := s.URL
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gone := closed.Addr().String()
	closed.Close()
	banner := raw(t, "SSH-2.0-"+pageSecret+"\r\n")
	header := raw(t, "HTTP/1.1 200 OK\r\n"+pageSecret+" has no colon\r\n\r\n")
	status := raw(t, "HTTP/1.1 "+pageSecret+" OK\r\n\r\n")
	silent := raw(t, "")
	const conn = "the policy does not allow connections to "

	tests := []struct {
		line   string
		status fact.Status
		detail string
	}{
		{"http 200 " + at + "/healthz", fact.Holds, at + "/healthz answered 200"},
		{"http 200 " + at + "/healthz ok", fact.Holds, at + `/healthz answered 200 and the body contains "ok"`},
		{"http 200 " + at + "/healthz degraded", fact.Broken, at + `/healthz answered 200, but the body does not contain "degraded"`},
		{"http 200 " + at + "/nothing", fact.Broken, at + "/nothing answered 404, not 200"},
		{"http 404 " + at + "/nothing", fact.Holds, at + "/nothing answered 404"},
		// The query string is sent and not shown.
		{"http 200 " + at + "/token?token=" + querySecret + " welcome", fact.Holds, at + `/token?… answered 200 and the body contains "welcome"`},
		{"http 200 " + at + "/token?token=wrong", fact.Broken, at + "/token?… answered 403, not 200"},
		// Only the first mebibyte of a body is searched.
		{"http 200 " + at + "/edge needle", fact.Holds, at + `/edge answered 200 and the body contains "needle"`},
		{"http 200 " + at + "/big needle", fact.Broken, at + `/big answered 200, but the body does not contain "needle"`},
		// A redirect is followed, and held to the same rules.
		{"http 200 " + at + "/moved ok", fact.Holds, at + `/moved answered 200 and the body contains "ok"`},
		{"http 200 " + at + "/with-password", fact.Broken, at + "/with-password did not answer: a redirect carried credentials"},
		{"http 200 " + at + "/bad-location", fact.Broken, at + "/bad-location did not answer: failed to parse Location header"},
		{"http 200 " + at + "/elsewhere", fact.Blocked, conn + "192.0.2.1. A person can allow it: holdfast allow net 192.0.2.1"},
		{"http 200 " + at + "/metadata", fact.Blocked, "169.254.169.254 is a link-local address (169.254.169.254), which is only allowed by its exact address. A person can allow it: holdfast allow net 169.254.169.254"},
		// Nobody there, or somebody who does not speak HTTP. What they sent
		// instead is not repeated.
		{"http 200 http://" + gone + "/", fact.Broken, "http://" + gone + "/ did not answer: connection refused"},
		{"http 200 http://" + banner + "/", fact.Broken, "http://" + banner + "/ did not answer: malformed HTTP response"},
		{"http 200 http://" + status + "/", fact.Broken, "http://" + status + "/ did not answer: malformed HTTP status code"},
		{"http 200 http://" + header + "/ ok", fact.Broken, "http://" + header + "/ did not answer: missing colon"},
		{"http 200 http://" + silent + "/", fact.Broken, "http://" + silent + "/ did not answer: EOF"},
		{"http 200 https://" + strings.TrimPrefix(at, "http://") + "/healthz", fact.Broken, "https://" + strings.TrimPrefix(at, "http://") + "/healthz did not answer: server gave HTTP response to HTTPS client"},

		{"port " + strings.TrimPrefix(at, "http://"), fact.Holds, strings.TrimPrefix(at, "http://") + " accepts connections"},
		{"port " + banner, fact.Holds, banner + " accepts connections"},
		{"port " + gone, fact.Broken, gone + " does not accept connections: connection refused"},

		// Outside what the default policy allows: not tried at all.
		{"http 200 http://192.0.2.1/", fact.Blocked, conn + "192.0.2.1. A person can allow it: holdfast allow net 192.0.2.1"},
		{"port 192.0.2.1:80", fact.Blocked, conn + "192.0.2.1. A person can allow it: holdfast allow net 192.0.2.1"},
		{"port [2001:db8::1]:80", fact.Blocked, conn + "2001:db8::1. A person can allow it: holdfast allow net 2001:db8::1"},
		{"http 200 https://example.com/", fact.Blocked, conn + "example.com. A person can allow it: holdfast allow net example.com"},
		{"port example.com:443", fact.Blocked, conn + "example.com. A person can allow it: holdfast allow net example.com"},
		{"dns example.com", fact.Blocked, conn + "example.com. A person can allow it: holdfast allow net example.com"},
	}
	for _, tt := range tests {
		both(t, o, parent, p, tt.line, tt.status, tt.detail)
	}
	if agent != "holdfast-test/1" {
		t.Errorf("the server saw the user agent %q", agent)
	}

	// A site with a certificate nobody vouches for is not trusted.
	tls := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	tls.Config.ErrorLog = log.New(io.Discard, "", 0)
	tls.StartTLS()
	defer tls.Close()
	judged(t, tls.Listener.Addr().String())
	both(t, o, parent, p, "http 200 "+tls.URL+"/", fact.Broken, tls.URL+"/ did not answer: the TLS certificate was not accepted")

	// A caller that stops waiting gets "not checked", never a verdict.
	ctx, cancel := context.WithCancel(bg)
	cancel()
	const stopped = "could not check: the check was stopped before it finished"
	for _, line := range []string{"port " + banner, "not port " + banner, "http 200 " + at + "/healthz", "not http 200 " + at + "/healthz", "not port " + gone} {
		if ev := Run(ctx, o, p, parse(t, line)); ev.Status != fact.Blocked || ev.Detail != stopped {
			t.Errorf("%s under a stopped caller\n got %s: %s\nwant blocked: %s", line, ev.Status, ev.Detail, stopped)
		}
	}
}

// judged has the machine judge one certificate before a check that is timed
// does. The first judgement after a start loads the list of authorities the
// machine trusts, and on a macOS machine that has only just started that alone
// has taken longer than the five seconds a check is given. The check would
// then say, truthfully, that nobody answered in time. What is tested above is
// what it says about the certificate.
func judged(t *testing.T, addr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(bg, time.Minute)
	defer cancel()
	c, err := (&tls.Dialer{}).DialContext(ctx, "tcp", addr)
	if err == nil {
		c.Close()
		t.Fatalf("a certificate nobody vouches for was accepted from %s", addr)
	}
}

func TestRealCommands(t *testing.T) {
	for _, prog := range []string{"sh", "true", "false", "echo", "sleep"} {
		if _, err := exec.LookPath(prog); err != nil {
			t.Skipf("%s is not installed here", prog)
		}
	}
	o, parent := project(t)
	p, err := policy.Parse("files .\ncmd true\ncmd false\ncmd echo **\ncmd sh -c *\ncmd sleep *\ncmd holdfast-no-such-program\ncmd ./tool\n")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOLDFAST_TEST_TOKEN", fileSecret)
	if err := os.WriteFile(filepath.Join(o.Root, "tool"), []byte("#!/bin/sh\necho from the project folder\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const refused = "the policy does not allow the command: "
	tests := []struct {
		line   string
		status fact.Status
		detail string
	}{
		{"cmd true", fact.Holds, "true exited 0"},
		{"cmd false", fact.Broken, "false exited 1"},
		{"says hello echo hello world", fact.Holds, `echo hello world printed "hello"`},
		{"says goodbye echo hello world", fact.Broken, `echo hello world exited 0 but did not print "goodbye" (it printed: hello world)`},
		{"says x true", fact.Broken, `true exited 0 but did not print "x" (it printed: nothing)`},
		{`cmd sh -c "echo oops >&2; exit 4"`, fact.Broken, "sh -c echo oops >&2; exit 4 exited 4: oops"},
		{`says oops sh -c "echo oops >&2"`, fact.Holds, `sh -c echo oops >&2 printed "oops"`}, // both streams count
		// No shell reads the arguments, and the command sees none of our settings.
		{"says $HOME echo $HOME", fact.Holds, `echo $HOME printed "$HOME"`},
		{`says [] sh -c "echo [$HOLDFAST_TEST_TOKEN]"`, fact.Holds, `sh -c echo [$HOLDFAST_TEST_TOKEN] printed "[]"`},
		// Only the first 64 KiB of output are kept.
		{`says needle sh -c "head -c 65536 /dev/zero | tr '\\0' x; echo needle"`, fact.Broken, ""},
		// Allowed, and cannot be started: nothing is known.
		{"cmd holdfast-no-such-program", fact.Blocked, `could not check: holdfast-no-such-program could not be run: exec: "holdfast-no-such-program": executable file not found in $PATH`},
		{"cmd ./tool", fact.Blocked, "could not check: ./tool could not be run: fork/exec ./tool: permission denied"},
		// Not allowed: not run.
		{"cmd sh -c id extra", fact.Blocked, refused + "sh -c id extra. A person can allow it: holdfast allow cmd sh -c id extra"},
		{"cmd sh", fact.Blocked, refused + "sh. A person can allow it: holdfast allow cmd sh"},
		{`cmd rm -rf "my notes"`, fact.Blocked, refused + `rm -rf "my notes". A person can allow it: holdfast allow cmd rm -rf 'my notes'`},
	}
	for _, tt := range tests {
		if tt.detail == "" {
			ev := Run(bg, o, p, parse(t, tt.line))
			if ev.Status != tt.status {
				t.Errorf("%s = %s: %s; want %s", tt.line, ev.Status, ev.Detail, tt.status)
			}
			oneLine(t, tt.line, ev)
			continue
		}
		both(t, o, parent, p, tt.line, tt.status, tt.detail)
	}

	// A caller that stops waiting: the command is not left running, and the
	// answer is "not checked".
	ctx, cancel := context.WithCancel(bg)
	cancel()
	start := time.Now()
	for _, line := range []string{"cmd sleep 30", "not cmd sleep 30", "not says x sleep 30"} {
		ev := Run(ctx, o, p, parse(t, line))
		if ev.Status != fact.Blocked || ev.Detail != "could not check: the check was stopped before it finished" {
			t.Errorf("%s under a stopped caller = %s: %s", line, ev.Status, ev.Detail)
		}
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("stopped checks took %v to come back", took)
	}
}

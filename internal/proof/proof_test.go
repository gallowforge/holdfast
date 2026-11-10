package proof

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/sim"
)

var (
	noon = time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	bg   = context.Background()
)

// Text that must never show up in evidence: it is in a file, in a page or in a
// query string, and no check searched for it.
const (
	fileSecret  = "hunter2-file-secret"
	pageSecret  = "hunter2-page-secret"
	querySecret = "hunter2-query-secret"
)

const makefile = "build:\n\tgo build ./...\n\nrelease: build\n\t./ship\n"

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func addrs(list ...string) []netip.Addr {
	var out []netip.Addr
	for _, s := range list {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

// machine is a pretend machine with something for every kind of check.
func machine() *sim.World {
	w := sim.New(func() time.Time { return noon })
	w.Write("go.mod", "module example\n\ngo 1.24\n", noon.Add(-time.Hour))
	w.Write("Makefile", makefile, noon.Add(-time.Hour))
	w.Write("LICENSE", "MIT", noon.Add(-time.Hour))
	w.Write("secrets.env", "DEBUG=false\nAPI_KEY="+fileSecret+"\n", noon)
	w.Write("backups/latest.tar", "tar", noon.Add(-3*time.Hour-10*time.Minute))
	w.Write("backups/old.tar", "tar", noon.Add(-76*time.Hour))
	w.Write("backups/edge.tar", "tar", noon.Add(-26*time.Hour))
	w.Write("backups/tomorrow.tar", "tar", noon.Add(time.Hour))
	w.Names["example.com"] = addrs("93.184.216.34", "2606:2800:220:1::1")
	w.Names["db.internal"] = addrs("10.0.0.5")
	w.Names["many.test"] = addrs("192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5", "192.0.2.6")
	w.Names["meta.test"] = addrs("169.254.169.254")
	w.Names["pub.test"] = addrs("203.0.113.9")
	w.Ports["localhost:8080"] = true
	w.Ports["example.com:443"] = true
	w.Ports["db.internal:5432"] = true
	w.Ports["meta.test:80"] = true
	w.Ports["pub.test:443"] = true
	w.Pages["http://localhost:8080/healthz"] = sim.Page{Status: 200, Body: "ok\nsession=" + pageSecret}
	w.Pages["http://localhost:8080/down"] = sim.Page{Status: 503, Body: "maintenance " + pageSecret}
	w.Pages["https://example.com/"] = sim.Page{Status: 200, Body: "<h1>Example Domain</h1>"}
	w.Cmds["systemctl is-active docker"] = sim.Result{Out: "active\n"}
	w.Cmds["systemctl is-active nginx"] = sim.Result{Exit: 3, Out: "inactive\n"}
	w.Cmds["true"] = sim.Result{}
	w.Cmds["false"] = sim.Result{Exit: 1}
	w.Cmds["make test"] = sim.Result{Exit: 2, Out: "\n\n  \x1b[31mFAIL\x1b[0m\tpkg/a\nmore\nlines\n"}
	return w
}

// open is a policy wide enough for everything on the pretend machine.
func open() *policy.Policy {
	p, err := policy.Parse("files .\nnet loopback private public\ncmd systemctl is-active *\ncmd true\ncmd false\ncmd make test\ncmd missing-program\n")
	if err != nil {
		panic(err)
	}
	return p
}

func parse(t *testing.T, line string) *fact.Check {
	t.Helper()
	c, err := fact.ParseCheck(line)
	if err != nil {
		t.Fatalf("ParseCheck(%s): %v", line, err)
	}
	return c
}

// oneLine checks the promises every piece of evidence keeps.
func oneLine(t *testing.T, line string, ev fact.Evidence) {
	t.Helper()
	if ev.Detail == "" || strings.ContainsAny(ev.Detail, "\n\r\t\x1b") || utf8.RuneCountInString(ev.Detail) > fact.MaxDetail {
		t.Errorf("%s: the evidence is not one plain line of at most %d letters: %q", line, fact.MaxDetail, ev.Detail)
	}
	for _, secret := range []string{fileSecret, pageSecret, querySecret} {
		if strings.Contains(ev.Detail, secret) {
			t.Errorf("%s: the evidence gives away %s: %q", line, secret, ev.Detail)
		}
	}
}

// Every kind of check, when it holds and when it is broken, with the exact
// line of evidence. The same check with "not" in front sees the same thing
// and concludes the opposite.
func TestEveryKind(t *testing.T) {
	license := sum("MIT")
	shortSum := license[:8] + "…" + license[60:]
	other := strings.Repeat("ab", 32)
	tests := []struct {
		line   string
		holds  bool
		detail string
	}{
		{"file go.mod", true, "go.mod exists"},
		{"file backups", true, "backups exists"}, // a folder exists too
		{"file ./backups/../go.mod", true, "./backups/../go.mod exists"},
		{"file missing.txt", false, "missing.txt does not exist"},
		{"file backups/missing.tar", false, "backups/missing.tar does not exist"},

		{`contains go.mod "go 1.24"`, true, `go.mod contains "go 1.24" (line 3)`},
		{"contains go.mod module", true, `go.mod contains "module" (line 1)`},
		{"contains secrets.env API_KEY", true, `secrets.env contains "API_KEY" (line 2)`},
		{`contains go.mod "go 1.25"`, false, `go.mod does not contain "go 1.25"`},
		{"contains go.mod MODULE", false, `go.mod does not contain "MODULE"`}, // letter case counts
		{"contains missing.txt x", false, "missing.txt does not exist"},

		// A pattern is read line by line, as grep reads it, and shown as written.
		{`matches Makefile "^release:"`, true, `Makefile matches "^release:" (line 4)`},
		{`matches Makefile '^build:$'`, true, `Makefile matches "^build:$" (line 1)`},
		{`matches Makefile 'ship$'`, true, `Makefile matches "ship$" (line 5)`},
		{`matches go.mod '^go 1\.\d+$'`, true, `go.mod matches "^go 1\.\d+$" (line 3)`},
		{`matches secrets.env 'API_KEY=.+'`, true, `secrets.env matches "API_KEY=.+" (line 2)`},
		{`matches Makefile '\Abuild'`, true, `Makefile matches "\Abuild" (line 1)`},
		{`matches Makefile '\Arelease'`, false, `Makefile does not match "\Arelease"`}, // \A is still the start of the file
		{`matches Makefile "^ship"`, false, `Makefile does not match "^ship"`},
		{`matches Makefile "^release:$"`, false, `Makefile does not match "^release:$"`},
		{"matches missing.txt x", false, "missing.txt does not exist"},

		{"unchanged LICENSE " + license, true, "LICENSE is unchanged (sha256 " + shortSum + ")"},
		{"unchanged LICENSE " + other, false, "LICENSE has changed (sha256 was abababab…abab, is now " + shortSum + ")"},
		{"unchanged missing.txt " + other, false, "missing.txt does not exist any more"},

		{"fresh backups/latest.tar 26h", true, "backups/latest.tar was modified 3h 10m ago, within 26h"},
		{"fresh backups/latest.tar 4h", true, "backups/latest.tar was modified 3h 10m ago, within 4h"},
		{"fresh backups/edge.tar 26h", true, "backups/edge.tar was modified 1d 2h ago, within 26h"}, // exactly as old as allowed
		{"fresh backups/tomorrow.tar 90s", true, "backups/tomorrow.tar was modified 0s ago, within 90s"},
		{"fresh backups/old.tar 26h", false, "backups/old.tar was last modified 3d 4h ago, more than 26h"},
		{"fresh backups/latest.tar 3h", false, "backups/latest.tar was last modified 3h 10m ago, more than 3h"},
		{"fresh backups/old.tar 2d", false, "backups/old.tar was last modified 3d 4h ago, more than 2d"},
		{"fresh backups/missing.tar 1w", false, "backups/missing.tar does not exist"},

		{"http 200 http://localhost:8080/healthz", true, "http://localhost:8080/healthz answered 200"},
		{"http 503 http://localhost:8080/down", true, "http://localhost:8080/down answered 503"},
		{"http 404 http://localhost:8080/nothing-here", true, "http://localhost:8080/nothing-here answered 404"},
		{"http 200 http://localhost:8080/healthz ok", true, `http://localhost:8080/healthz answered 200 and the body contains "ok"`},
		{`http 200 https://example.com/ "Example Domain"`, true, `https://example.com/ answered 200 and the body contains "Example Domain"`},
		{"http 200 https://example.com", true, "https://example.com answered 200"},
		{"http 200 http://localhost:8080/down", false, "http://localhost:8080/down answered 503, not 200"},
		{"http 200 http://localhost:8080/nothing-here", false, "http://localhost:8080/nothing-here answered 404, not 200"},
		{"http 200 http://localhost:8080/healthz degraded", false, `http://localhost:8080/healthz answered 200, but the body does not contain "degraded"`},
		{"http 200 http://localhost:8080/down maintenance", false, "http://localhost:8080/down answered 503, not 200"}, // the status is looked at first
		{"http 200 http://localhost:9999/", false, "http://localhost:9999/ did not answer: connection refused"},
		{"http 200 https://nowhere.test/", false, "https://nowhere.test/ did not answer: the name does not exist"},
		// The query string is the part most likely to be private. It is sent,
		// and it is not shown.
		{"http 200 http://localhost:8080/healthz?token=" + querySecret + "#top", true, "http://localhost:8080/healthz?… answered 200"},
		{"http 200 http://localhost:8080/healthz?token=" + querySecret + " degraded", false, `http://localhost:8080/healthz?… answered 200, but the body does not contain "degraded"`},
		{"http 204 http://localhost:8080/down?token=" + querySecret, false, "http://localhost:8080/down?… answered 503, not 204"},
		{"http 200 http://localhost:9999/?token=" + querySecret, false, "http://localhost:9999/?… did not answer: connection refused"},

		{"port localhost:8080", true, "localhost:8080 accepts connections"},
		{"port db.internal:5432", true, "db.internal:5432 accepts connections"},
		{"port localhost:9999", false, "localhost:9999 does not accept connections: connection refused"},
		{"port 127.0.0.1:8080", false, "127.0.0.1:8080 does not accept connections: connection refused"},
		{"port nowhere.test:80", false, "nowhere.test:80 does not accept connections: the name does not exist"},

		{"dns example.com", true, "example.com resolves to 93.184.216.34, 2606:2800:220:1::1"},
		{"dns Example.COM.", true, "example.com resolves to 93.184.216.34, 2606:2800:220:1::1"},
		{"dns example.com 93.184.216.34", true, "example.com resolves to 93.184.216.34, 2606:2800:220:1::1"},
		{"dns example.com 2606:2800:220:1:0:0:0:1", true, "example.com resolves to 93.184.216.34, 2606:2800:220:1::1"},
		{"dns example.com ::ffff:93.184.216.34", true, "example.com resolves to 93.184.216.34, 2606:2800:220:1::1"},
		{"dns localhost 127.0.0.1", true, "localhost resolves to 127.0.0.1"},
		{"dns many.test", true, "many.test resolves to 192.0.2.1, 192.0.2.2, 192.0.2.3, 192.0.2.4, and 2 more"},
		{"dns many.test 192.0.2.6", true, "many.test resolves to 192.0.2.1, 192.0.2.2, 192.0.2.3, 192.0.2.4, and 2 more"},
		{"dns example.com 10.0.0.1", false, "example.com resolves to 93.184.216.34, 2606:2800:220:1::1, not 10.0.0.1"},
		{"dns many.test 192.0.2.7", false, "many.test resolves to 192.0.2.1, 192.0.2.2, 192.0.2.3, 192.0.2.4, and 2 more, not 192.0.2.7"},
		{"dns nowhere.test", false, "nowhere.test does not resolve: the name does not exist"},
		{"dns nowhere.test 10.0.0.1", false, "nowhere.test does not resolve: the name does not exist"},

		{"cmd systemctl is-active docker", true, "systemctl is-active docker exited 0"},
		{"cmd true", true, "true exited 0"},
		{"cmd systemctl is-active nginx", false, "systemctl is-active nginx exited 3: inactive"},
		{"cmd false", false, "false exited 1"},
		// Only the first line that says something, without what would colour a terminal.
		{"cmd make test", false, "make test exited 2: [31mFAIL[0m pkg/a"},

		{"says active systemctl is-active docker", true, `systemctl is-active docker printed "active"`},
		{"says tive systemctl is-active docker", true, `systemctl is-active docker printed "tive"`},
		{"says running systemctl is-active docker", false, `systemctl is-active docker exited 0 but did not print "running" (it printed: active)`},
		{"says ACTIVE systemctl is-active docker", false, `systemctl is-active docker exited 0 but did not print "ACTIVE" (it printed: active)`},
		{"says ok true", false, `true exited 0 but did not print "ok" (it printed: nothing)`},
		// A command that fails has not said it, whatever it printed.
		{"says inactive systemctl is-active nginx", false, "systemctl is-active nginx exited 3: inactive"},
		{"says FAIL make test", false, "make test exited 2: [31mFAIL[0m pkg/a"},
	}
	w, p := machine(), open()
	seen := map[string]int{}
	for _, tt := range tests {
		for _, not := range []bool{false, true} {
			line := tt.line
			want := fact.Broken
			if not {
				line = "not " + line
			}
			if tt.holds != not {
				want = fact.Holds
			}
			c := parse(t, line)
			ev := Run(bg, w, p, c)
			if ev.Status != want || ev.Detail != tt.detail {
				t.Errorf("%s\n got %s: %s\nwant %s: %s", line, ev.Status, ev.Detail, want, tt.detail)
			}
			oneLine(t, line, ev)
			if !ev.At.Equal(noon) || ev.At.Location() != time.UTC {
				t.Errorf("%s: seen at %v, want the machine's time %v", line, ev.At, noon)
			}
			if took := w.Took(c.Kind, target(c)).Milliseconds(); ev.Ms != took {
				t.Errorf("%s: took %d ms, want the pretend machine's %d ms", line, ev.Ms, took)
			}
			if !not && tt.holds {
				seen[c.Kind] |= 1
			} else if !not {
				seen[c.Kind] |= 2
			}
		}
	}
	for _, kind := range fact.Kinds {
		if seen[kind] != 3 {
			t.Errorf("%s: the table needs a check that holds and one that is broken", kind)
		}
	}
}

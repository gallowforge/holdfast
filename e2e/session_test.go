//go:build unix

package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The session in the README, start to finish: store a fact with its proof,
// change the thing it is about, and watch the memory notice.
func TestAPersonAtTheCommandLine(t *testing.T) {
	p := newProject(t)

	out := p.run(0, "add", "The API listens on port 8080", "--check", `contains .env "PORT=8080"`, "--tag", "api")
	has(t, out, "Stored, and proved just now.", "HOLDS", `.env contains "PORT=8080" (line 1)`)
	port := id(t, out)

	// A claim that is not true right now is not a fact, and is turned away.
	out = p.run(1, "add", "The API listens on port 9999", "--check", `contains .env "PORT=9999"`)
	has(t, out, "Not stored")

	out = p.run(0, "add", "Deploys go out on Fridays")
	has(t, out, "Stored as a note", "UNPROVEN")
	note := id(t, out)

	// A check that reaches past the policy is kept, and not run.
	out = p.run(0, "add", "Staging is up", "--check", "http 200 https://staging.example.com/healthz")
	has(t, out, "BLOCKED", "holdfast allow net staging.example.com")

	has(t, p.run(0, "list"), "3 facts: 1 blocked, 1 unproven, 1 holds.")

	// The file changes. The very next question gets the new answer, with no
	// "prove it again" in between: that is the promise.
	p.write(".env", "PORT=9090\n")
	out = p.run(0, "recall", "api port")
	has(t, out, "BROKEN", port, `.env does not contain "PORT=8080"`, "Do not rely on it")

	// check is the same thing for a script: it exits 1 while a fact is broken.
	has(t, p.run(1, "check"), "1 broken", "BROKEN")
	var checked struct {
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal([]byte(p.run(1, "check", "--json")), &checked); err != nil {
		t.Fatalf("check --json does not print JSON: %v", err)
	}
	// check counts the facts that have a check; the note is not one of them.
	if checked.Counts["broken"] != 1 || checked.Counts["blocked"] != 1 || checked.Counts["total"] != 2 {
		t.Errorf("check --json counts %v, want 1 broken and 1 blocked of 2", checked.Counts)
	}

	// What is true now replaces what was, and the old fact stays in the history.
	out = p.run(0, "add", "The API listens on port 9090", "--check", `contains .env "PORT=9090"`, "--replaces", port)
	has(t, out, "Stored, and proved just now.")
	has(t, p.run(0, "why", port), "replaced by "+id(t, out))
	p.run(0, "check")

	has(t, p.run(0, "retract", note, "--reason", "only a test"), note)
	has(t, p.run(0, "export"), "# What Holdfast knows", "The API listens on port 9090")
	has(t, p.run(0, "brief"), "The API listens on port 9090")

	p.run(1, "why", "f-zzzzzz")
	has(t, p.run(0, "version"), "holdfast")
}

// An agent on the older revisions of the protocol, which begin with a
// handshake: every released Claude Code, Cursor and Codex today.
func TestAnAgentOverThePipe(t *testing.T) {
	p := newProject(t)
	a := p.serveAgent("claude-code")
	a.hello("2025-11-25")

	rep := a.ask("tools/list", nil)
	var listed struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rep.Result, &listed); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" || len(tool.InputSchema) == 0 {
			t.Errorf("the tool %s comes without a description or a schema", tool.Name)
		}
	}
	sort.Strings(names)
	if got := strings.Join(names, " "); got != "recall remember retract verify" {
		t.Fatalf("the tools are %q", got)
	}

	stored := a.call(nil, "remember", map[string]any{
		"claim": "The API listens on port 8080",
		"check": `contains .env "PORT=8080"`,
		"tags":  []string{"api"},
	})
	if stored.Failed || stored.Fact.Status != "holds" || stored.Fact.By != "claude-code" {
		t.Fatalf("remember gave %+v\n%s", stored.Fact, stored.Text)
	}

	// The world moves under the agent while it works.
	p.write(".env", "PORT=9090\n")
	asked := a.call(nil, "recall", map[string]any{"query": "which port does the api listen on"})
	if len(asked.Facts) != 1 || asked.Facts[0].ID != stored.Fact.ID || asked.Facts[0].Status != "broken" {
		t.Fatalf("recall gave %+v, want the port fact, broken\n%s", asked.Facts, asked.Text)
	}
	has(t, asked.Text, "BROKEN", "Do not rely on it")

	fixed := a.call(nil, "remember", map[string]any{
		"claim":    "The API listens on port 9090",
		"check":    `contains .env "PORT=9090"`,
		"replaces": stored.Fact.ID,
	})
	if fixed.Failed || fixed.Fact.Status != "holds" {
		t.Fatalf("the correction gave %+v\n%s", fixed.Fact, fixed.Text)
	}

	// An agent can propose any check. What it cannot do is make one run.
	if err := os.WriteFile(filepath.Join(filepath.Dir(p.dir), "outside.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ claim, check, fix string }{
		{"A program can be run", "cmd touch made-by-a-check", "holdfast allow cmd"},
		{"The file next door can be read", "contains ../outside.txt secret", "holdfast allow files"},
		{"The wider network answers", "http 200 https://staging.example.com/", "holdfast allow net staging.example.com"},
	} {
		got := a.call(nil, "remember", map[string]any{"claim": c.claim, "check": c.check})
		if got.Fact.Status != "blocked" {
			t.Errorf("%q came back %s, want blocked\n%s", c.check, got.Fact.Status, got.Text)
		}
		has(t, got.Text, "BLOCKED", c.fix)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "made-by-a-check")); err == nil {
		t.Fatal("a command the policy does not allow was run")
	}

	all := a.call(nil, "verify", nil)
	if all.Counts["total"] != 4 || all.Counts["holds"] != 1 || all.Counts["blocked"] != 3 || all.Counts["broken"] != 0 {
		t.Errorf("verify counts %v, want 1 holding and 3 blocked of 4", all.Counts)
	}

	gone := a.call(nil, "retract", map[string]any{"id": fixed.Fact.ID, "reason": "only a test"})
	if gone.Failed {
		t.Errorf("retract was turned down: %s", gone.Text)
	}
	if after := a.call(nil, "recall", map[string]any{"query": "api port"}); len(after.Facts) != 0 {
		t.Errorf("a retracted fact was recalled: %+v", after.Facts)
	}

	// What the agent stored is on disk for the person, and for the next agent.
	a.bye()
	has(t, p.run(0, "list"), "3 facts: 3 blocked.")
	has(t, p.run(0, "why", fixed.Fact.ID), "retracted", "only a test")
}

// The 2026-07-28 revision has no handshake: each request says which revision
// it speaks and who is asking. The same binary answers both kinds of client.
func TestAnAgentWithoutAHandshake(t *testing.T) {
	p := newProject(t)
	a := p.serveAgent("")
	meta := map[string]any{
		"io.modelcontextprotocol/protocolVersion": "2026-07-28",
		"io.modelcontextprotocol/clientInfo":      map[string]any{"name": "cursor", "version": "3.0"},
	}

	rep := a.ask("server/discover", map[string]any{"_meta": meta})
	if rep.Error != nil {
		t.Fatalf("server/discover was refused: %s", rep.Error.Message)
	}
	var found struct {
		Versions []string `json:"supportedVersions"`
	}
	if err := json.Unmarshal(rep.Result, &found); err != nil || len(found.Versions) < 2 || found.Versions[0] != "2026-07-28" {
		t.Fatalf("server/discover lists %v", found.Versions)
	}

	stored := a.call(meta, "remember", map[string]any{"claim": "The API listens on port 8080", "check": `contains .env "PORT=8080"`})
	if stored.Failed || stored.Fact.Status != "holds" {
		t.Fatalf("remember gave %+v\n%s", stored.Fact, stored.Text)
	}
	if asked := a.call(meta, "recall", map[string]any{"query": "port"}); len(asked.Facts) != 1 || asked.Facts[0].Status != "holds" {
		t.Errorf("recall gave %+v", asked.Facts)
	}

	// A revision nobody has heard of is refused, with the ones that work.
	bad := a.ask("tools/list", map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2031-01-01"}})
	if bad.Error == nil || bad.Error.Code != -32022 || !strings.Contains(string(bad.Error.Data), "2026-07-28") {
		t.Errorf("an unknown revision got %+v", bad.Error)
	}
	a.bye()
}

// A fact is shared; proof is local. The facts travel with the repository, and
// each machine proves them against itself.
func TestFactsTravelAndProofStaysHome(t *testing.T) {
	mine := newProject(t)
	out := mine.run(0, "add", "The API listens on port 8080", "--check", `contains .env "PORT=8080"`)
	port := id(t, out)

	// A colleague clones the project. Their checkout says something else.
	theirs := &project{t: t, dir: filepath.Join(t.TempDir(), "app"), home: filepath.Join(t.TempDir(), "home")}
	for _, name := range []string{"facts.jsonl", "policy", ".gitignore", ".gitattributes"} {
		text, err := os.ReadFile(filepath.Join(mine.dir, ".holdfast", name))
		if err != nil {
			t.Fatal(err)
		}
		theirs.write(filepath.Join(".holdfast", name), string(text))
	}
	theirs.write(".env", "PORT=3000\n")

	// The fact arrived. No proof came with it.
	has(t, theirs.run(0, "list"), port, "BLOCKED", "has not run on this machine yet")
	has(t, theirs.run(1, "check"), "BROKEN", port)
	// And nothing they found changes what holds on the first machine.
	has(t, mine.run(0, "check"), "1 fact: 1 holds.")
}

// The ledger in the browser: it answers its own page, and nobody else's.
func TestTheLedgerAnswersOnlyItsOwnPage(t *testing.T) {
	p := newProject(t)
	p.run(0, "add", "The API listens on port 8080", "--check", `contains .env "PORT=8080"`)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	srv := p.command("serve", "--addr", addr)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- srv.Wait() }()
	t.Cleanup(func() {
		if srv.ProcessState == nil {
			srv.Process.Kill()
		}
	})

	post := func(headers map[string]string) (int, string) {
		t.Helper()
		req, err := http.NewRequest("POST", "http://"+addr+"/api/state", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer res.Body.Close()
		var body struct {
			OK     bool `json:"ok"`
			Result struct {
				Counts map[string]int `json:"counts"`
			} `json:"result"`
		}
		json.NewDecoder(res.Body).Decode(&body)
		return res.StatusCode, fmt.Sprint(body.OK, " ", body.Result.Counts["total"])
	}

	up := false
	for range 100 {
		if code, _ := post(map[string]string{"X-Holdfast": "1"}); code != 0 {
			up = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !up {
		t.Fatal("the ledger did not come up within 5 seconds")
	}

	if code, got := post(map[string]string{"X-Holdfast": "1"}); code != 200 || got != "true 1" {
		t.Errorf("the page's own request got %d (%s), want 200 with one fact", code, got)
	}
	// A form on some other site cannot set the header, so it gets nothing.
	if code, _ := post(nil); code != 403 {
		t.Errorf("a request without the header got %d, want 403", code)
	}
	// A script on some other site that could set it is still not this page.
	if code, _ := post(map[string]string{"X-Holdfast": "1", "Origin": "https://example.com"}); code != 403 {
		t.Errorf("a request from another site got %d, want 403", code)
	}

	srv.Process.Signal(syscall.SIGINT)
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("the ledger did not close cleanly on Ctrl+C: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the ledger was still running 10 seconds after Ctrl+C")
	}

	// Past this machine, it listens only when a person says so in plain words.
	has(t, p.run(2, "serve", "--addr", "0.0.0.0:0"), "--expose")
}

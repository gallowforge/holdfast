package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/engine"
	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/serve"
	"github.com/gallowforge/holdfast/internal/store"
	"github.com/gallowforge/holdfast/internal/web"
)

// page is what a running command has written so far. It can be read while
// the command is still writing.
type page struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (p *page) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.Write(b)
}

func (p *page) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.String()
}

// agent is a client of "holdfast mcp": it sends one request, reads one
// answer, and hangs up when it is done, as a real one does.
type agent struct {
	t     *testing.T
	in    *io.PipeWriter
	lines chan string
	ended chan int
	errs  *page
	id    int
	once  sync.Once
	code  int
}

// agent starts "holdfast mcp" at this desk.
func (d *desk) agent(args ...string) *agent {
	d.t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	g := &agent{t: d.t, in: inW, lines: make(chan string, 64), ended: make(chan int, 1), errs: &page{}}
	a := &app{in: inR, out: outW, err: g.errs, cwd: d.cwd, env: func(name string) string { return d.env[name] }}
	go func() {
		code := a.run(append([]string{"mcp"}, args...))
		// Nobody reads or writes any more: a test that goes on talking is
		// told so instead of waiting for ever.
		inR.Close()
		outW.Close()
		g.ended <- code
	}()
	go func() {
		defer close(g.lines)
		r := bufio.NewReader(outR)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				g.lines <- line
			}
			if err != nil {
				return
			}
		}
	}()
	d.t.Cleanup(func() { g.hangUp() })
	return g
}

// send writes one line to the server.
func (g *agent) send(line string) {
	g.t.Helper()
	if _, err := io.WriteString(g.in, line+"\n"); err != nil {
		g.t.Fatalf("holdfast mcp is no longer reading (%v); it said: %s", err, g.errs)
	}
}

// next is the server's next answer.
func (g *agent) next() map[string]any {
	g.t.Helper()
	select {
	case line, open := <-g.lines:
		if !open {
			g.t.Fatalf("holdfast mcp hung up without answering; it said: %s", g.errs)
		}
		var got map[string]any
		decode(g.t, line, &got)
		return got
	case <-time.After(20 * time.Second):
		g.t.Fatal("holdfast mcp did not answer within 20 s")
	}
	return nil
}

// ask sends a request and returns the result it is answered with.
func (g *agent) ask(method string, params any) map[string]any {
	g.t.Helper()
	g.id++
	msg := map[string]any{"jsonrpc": "2.0", "id": g.id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	data, err := json.Marshal(msg)
	if err != nil {
		g.t.Fatal(err)
	}
	g.send(string(data))
	got := g.next()
	if got["id"] != float64(g.id) || got["error"] != nil {
		g.t.Fatalf("%s was answered with %v", method, got)
	}
	result, _ := got["result"].(map[string]any)
	return result
}

// hello opens the conversation the way a client does.
func (g *agent) hello(name string) {
	g.t.Helper()
	got := g.ask("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": name, "version": "1.0"},
	})
	if info, _ := got["serverInfo"].(map[string]any); info["name"] != "holdfast" || info["version"] != version {
		g.t.Fatalf("the server introduced itself as %v", got["serverInfo"])
	}
	g.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
}

// use calls one tool and returns what the agent reads, whether it is marked
// as an error, and the same answer as data.
func (g *agent) use(tool string, args map[string]any) (text string, failed bool, data map[string]any) {
	g.t.Helper()
	got := g.ask("tools/call", map[string]any{"name": tool, "arguments": args})
	content, _ := got["content"].([]any)
	if len(content) != 1 {
		g.t.Fatalf("%s answered with %d pieces of content: %v", tool, len(content), got)
	}
	first, _ := content[0].(map[string]any)
	text, _ = first["text"].(string)
	failed, _ = got["isError"].(bool)
	data, _ = got["structuredContent"].(map[string]any)
	return text, failed, data
}

// hangUp closes the client's end and says how the command ended.
func (g *agent) hangUp() int {
	g.once.Do(func() {
		g.in.Close()
		select {
		case g.code = <-g.ended:
		case <-time.After(20 * time.Second):
			g.code = -1
			g.t.Error("holdfast mcp did not end within 20 s of its input closing")
		}
	})
	return g.code
}

// author is who the store says remembered the claim.
func (d *desk) author(claim string, more ...string) string {
	d.t.Helper()
	for _, v := range d.listed(more...).Facts {
		if v.Claim == claim {
			return v.By
		}
	}
	d.t.Fatalf("no fact says %q", claim)
	return ""
}

// --- mcp ---

func TestMCPServesTheStoreOfTheProjectItIsStartedIn(t *testing.T) {
	d := project(t)
	known := d.add(goClaim, goCheck)

	g := d.agent()
	g.hello("claude-code")
	listed := g.ask("tools/list", nil)
	if tools, _ := listed["tools"].([]any); len(tools) < 2 {
		t.Errorf("the server offers %d tools", len(tools))
	}

	// What the command line stored, the agent recalls, proved again.
	text, failed, _ := g.use("recall", map[string]any{"query": "which go version"})
	if failed {
		t.Fatalf("recall failed: %s", text)
	}
	says(t, "recall", text, "HOLDS", known.ID, goClaim)

	// What the agent stores, the command line lists.
	const claim = "The module is example.com/atlas"
	text, failed, data := g.use("remember", map[string]any{"claim": claim, "check": `contains go.mod "module example.com/atlas"`, "tags": []string{"build"}})
	if failed || !strings.HasPrefix(text, "Stored, and proved just now.\n\nHOLDS     ") {
		t.Fatalf("remember answered (failed: %v):\n%s", failed, text)
	}
	stored, _ := data["fact"].(map[string]any)
	if stored["status"] != "holds" || stored["claim"] != claim {
		t.Errorf("remember's data says %v", stored)
	}
	// While the server is still running: the two share the files, not a process.
	l := d.listed()
	if l.Counts.Total != 2 || l.Counts.Holds != 2 {
		t.Errorf("the command line counts %+v, want 2 facts that hold", l.Counts)
	}
	if by := d.author(claim); by != "claude-code" {
		t.Errorf("the agent's fact is stored under %q, want the name the client gave", by)
	}
	other := d.add("Deploys go out on Fridays", "")
	if text, _, _ := g.use("recall", map[string]any{"query": "deploys"}); !strings.Contains(text, other.ID) {
		t.Errorf("the agent does not see what the command line stored meanwhile:\n%s", text)
	}

	// A check that fails is told to the agent as its own mistake to fix.
	text, failed, _ = g.use("remember", map[string]any{"claim": "Go is pinned to 1.99", "check": `contains go.mod "go 1.99"`})
	if !failed || !strings.HasPrefix(text, "the check fails right now, so this is not a fact yet: ") {
		t.Errorf("a claim whose check fails was answered (failed: %v):\n%s", failed, text)
	}
	if n := d.listed().Counts.Total; n != 3 {
		t.Errorf("after a refused claim the store holds %d facts, want 3", n)
	}

	// A line that is no request is answered and does not end the session.
	g.send("this is not JSON")
	if got := g.next(); got["error"] == nil {
		t.Errorf("a line that is not JSON was answered with %v", got)
	}
	if text, failed, _ := g.use("recall", map[string]any{"query": "module"}); failed || !strings.Contains(text, claim) {
		t.Errorf("after a bad line, recall answered (failed: %v):\n%s", failed, text)
	}

	if code := g.hangUp(); code != 0 {
		t.Errorf("when its input ended, holdfast mcp exited %d, want 0", code)
	}
	if said := g.errs.String(); said != "" {
		t.Errorf("an uneventful session logged: %s", said)
	}
}

func TestMCPStoresFactsUnderTheNameItIsGiven(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		env  string
		want string
	}{
		"the client's own name by default": {nil, "", "claude-code"},
		"--as":                             {[]string{"--as", "scout"}, "", "scout"},
		"HOLDFAST_AGENT":                   {nil, "reviewer", "reviewer"},
		"--as before HOLDFAST_AGENT":       {[]string{"--as", "scout"}, "reviewer", "scout"},
	} {
		t.Run(name, func(t *testing.T) {
			d := project(t)
			d.env["HOLDFAST_AGENT"] = c.env
			g := d.agent(c.args...)
			g.hello("claude-code")
			if text, failed, _ := g.use("remember", map[string]any{"claim": "Deploys go out on Fridays"}); failed {
				t.Fatalf("remember failed: %s", text)
			}
			if code := g.hangUp(); code != 0 {
				t.Fatalf("exit %d: %s", code, g.errs)
			}
			// Never the person's login name: it was not the person who said it.
			if by := d.author("Deploys go out on Fridays"); by != c.want {
				t.Errorf("stored under %q, want %q", by, c.want)
			}
		})
	}
}

func TestMCPCreatesTheStoreWhenTheFirstFactIsRemembered(t *testing.T) {
	d := sit(t)
	d.write("go.mod", goMod)
	g := d.agent()
	g.hello("claude-code")

	// Asking is not a reason to leave a folder behind.
	text, failed, _ := g.use("recall", map[string]any{"query": "go version"})
	if failed {
		t.Fatalf("recall where there is no store failed: %s", text)
	}
	if text, failed, _ := g.use("verify", nil); failed {
		t.Fatalf("verify where there is no store failed: %s", text)
	}
	if left := tree(t, d.cwd); !reflect.DeepEqual(left, []string{"go.mod"}) {
		t.Fatalf("reading from a store that is not there created %v", left)
	}

	text, failed, _ = g.use("remember", map[string]any{"claim": goClaim, "check": goCheck})
	if failed || !strings.HasPrefix(text, "Stored, and proved just now.") {
		t.Fatalf("the first fact was answered (failed: %v):\n%s", failed, text)
	}

	// The store is the one "holdfast init" would have made.
	made := folder(t)
	d.ok("init", "--dir", made)
	if got, want := tree(t, filepath.Join(d.cwd, ".holdfast")), tree(t, filepath.Join(made, ".holdfast")); !reflect.DeepEqual(got, want) {
		t.Errorf("the agent's store has %v, init makes %v", got, want)
	}
	if got, want := read(t, d.policyFile()), read(t, filepath.Join(made, ".holdfast", "policy")); got != want {
		t.Errorf("the agent's store has the policy:\n%s\ninit writes:\n%s", got, want)
	}
	if !d.inForce() {
		t.Error("the policy of the store the agent created is not in force")
	}
	if got := d.ok("init"); got.out != "Already set up: "+filepath.Join(d.cwd, ".holdfast")+"\n" {
		t.Errorf("init after the agent created the store said %q", got.out)
	}

	if text, _, _ := g.use("recall", map[string]any{"query": "go version"}); !strings.Contains(text, goClaim) {
		t.Errorf("the fact just stored is not recalled:\n%s", text)
	}
	if code := g.hangUp(); code != 0 || g.errs.String() != "" {
		t.Errorf("exit %d, logged %q", code, g.errs)
	}
	if l := d.listed(); l.Counts.Holds != 1 || d.author(goClaim) != "claude-code" {
		t.Errorf("the command line counts %+v", l.Counts)
	}
	if left := tree(t, d.home); len(left) != 0 {
		t.Errorf("a project's agent wrote %v in the home folder", left)
	}
}

func TestMCPNoInitRefusesToRememberWhereThereIsNoStore(t *testing.T) {
	d := sit(t)
	d.write("go.mod", goMod)
	g := d.agent("--no-init")
	g.hello("claude-code")

	refusal := "there is no Holdfast store in " + d.cwd + " yet, and this server was told not to create one. A person can create it: holdfast init"
	for name, args := range map[string]map[string]any{
		"a fact with a check": {"claim": goClaim, "check": goCheck},
		"a note":              {"claim": "Deploys go out on Fridays"},
	} {
		text, failed, data := g.use("remember", args)
		if !failed || text != refusal || data != nil {
			t.Errorf("%s was answered (failed: %v, data: %v):\n%s\nwant the refusal:\n%s", name, failed, data, text, refusal)
		}
	}
	if left := tree(t, d.cwd); !reflect.DeepEqual(left, []string{"go.mod"}) {
		t.Fatalf("a server told not to create a store left %v", left)
	}
	// Questions are still answered: there is simply nothing to tell.
	if text, failed, _ := g.use("recall", map[string]any{"query": "go"}); failed || strings.Contains(text, goClaim) {
		t.Errorf("recall answered (failed: %v):\n%s", failed, text)
	}

	// Once a person has created it, the same server uses it.
	d.ok("init")
	text, failed, _ := g.use("remember", map[string]any{"claim": goClaim, "check": goCheck})
	if failed || !strings.HasPrefix(text, "Stored, and proved just now.") {
		t.Errorf("after init, remember answered (failed: %v):\n%s", failed, text)
	}
	if code := g.hangUp(); code != 0 || g.errs.String() != "" {
		t.Errorf("exit %d, logged %q", code, g.errs)
	}
	if n := d.listed().Counts.Total; n != 1 {
		t.Errorf("the store holds %d facts, want the 1 stored after init", n)
	}
}

// The refusal names the command that creates the store the server is about,
// which is not always the one in the folder the person stands in.
func TestMCPNoInitNamesTheStoreAPersonWouldHaveToCreate(t *testing.T) {
	elsewhere := folder(t)
	for name, c := range map[string]struct {
		args []string
		in   func(d *desk) string
		hint string
	}{
		"--global": {[]string{"--global"}, func(d *desk) string { return d.home }, "holdfast init --global"},
		"--dir":    {[]string{"--dir", elsewhere}, func(*desk) string { return elsewhere }, "holdfast init --dir " + elsewhere},
	} {
		t.Run(name, func(t *testing.T) {
			d := sit(t)
			g := d.agent(append([]string{"--no-init"}, c.args...)...)
			g.hello("claude-code")
			text, failed, _ := g.use("remember", map[string]any{"claim": "Deploys go out on Fridays"})
			want := "there is no Holdfast store in " + c.in(d) + " yet, and this server was told not to create one. A person can create it: " + c.hint
			if !failed || text != want {
				t.Errorf("answered (failed: %v):\n%s\nwant:\n%s", failed, text, want)
			}
			// Following the hint is enough.
			d.ok(strings.Fields(strings.TrimPrefix(c.hint, "holdfast "))...)
			if text, failed, _ := g.use("remember", map[string]any{"claim": "Deploys go out on Fridays"}); failed {
				t.Errorf("after %q the server still refuses: %s", c.hint, text)
			}
			g.hangUp()
			if left := tree(t, d.cwd); len(left) != 0 {
				t.Errorf("%v was written in the folder the server was started in", left)
			}
		})
	}
}

// The store an agent creates for the whole machine starts as carefully as
// the one a person creates: nothing in the home folder may be read.
func TestMCPGlobalCreatesTheMachineStoreWithNoFolderToRead(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		home bool // started in the home folder itself
	}{
		"--global from a project folder": {[]string{"--global"}, false},
		"started in the home folder":     {nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			d := sit(t)
			d.write(filepath.Join(d.home, "notes.txt"), "remember the milk\n")
			started := d.cwd
			if c.home {
				d.cwd = d.home
			}
			g := d.agent(c.args...)
			g.hello("claude-code")
			text, failed, data := g.use("remember", map[string]any{"claim": "My notes exist", "check": "file notes.txt"})
			if failed || !strings.HasPrefix(text, "Stored, but not proved: the check may not run until a person allows it.") {
				t.Fatalf("answered (failed: %v):\n%s", failed, text)
			}
			if stored, _ := data["fact"].(map[string]any); stored["status"] != "blocked" {
				t.Errorf("the fact is %v, want blocked", stored["status"])
			}
			if code := g.hangUp(); code != 0 || g.errs.String() != "" {
				t.Errorf("exit %d, logged %q", code, g.errs)
			}
			// Nothing but the store is new in the home folder, and the store
			// is the one "holdfast init --global" makes.
			for _, name := range tree(t, d.home) {
				if name != "notes.txt" && !strings.HasPrefix(name, ".holdfast/") {
					t.Errorf("the home folder now holds %s, which is neither the store nor what was there", name)
				}
			}
			has := tree(t, filepath.Join(d.home, ".holdfast"))
			if got := d.ok("init", "--global"); got.out != "Already set up: "+filepath.Join(d.home, ".holdfast")+"\n" {
				t.Errorf("init --global after the agent created the store said %q", got.out)
			}
			if now := tree(t, filepath.Join(d.home, ".holdfast")); !reflect.DeepEqual(has, now) {
				t.Errorf("the agent's store had %v; init --global completed it to %v", has, now)
			}
			lacks(t, "the machine's policy", d.ok("policy", "--global").out, "\nfiles ")
			if left := tree(t, started); len(left) != 0 {
				t.Errorf("%v was written in the folder the server was started in", left)
			}
		})
	}
}

func TestMCPTakesFlagsOnly(t *testing.T) {
	d := project(t)
	got := d.run("mcp", "serve")
	if got.code != 2 || got.out != "" || !strings.HasPrefix(got.err, "holdfast: mcp takes no arguments\nUsage: holdfast mcp [--as <name>] [--no-init]\n") {
		t.Errorf("mcp with an argument: exit %d, %q, %q", got.code, got.out, got.err)
	}
	got = d.run("mcp", "--help")
	if got.code != 0 || got.out != "" || !strings.HasPrefix(got.err, "Usage: holdfast mcp [--as <name>] [--no-init]\n") {
		t.Errorf("mcp --help: exit %d, %q, %q", got.code, got.out, got.err)
	}
	// Nothing but answers may go to the client: it reads every line as one.
	if got = d.run("mcp", "--verbose"); got.code != 2 || got.out != "" {
		t.Errorf("mcp with a flag it does not have: exit %d, %q", got.code, got.out)
	}
	// With no client at the other end there is nothing to do.
	if got = d.run("mcp"); got.code != 0 || got.out != "" || got.err != "" {
		t.Errorf("mcp with no input: exit %d, %q, %q", got.code, got.out, got.err)
	}
}

// --- the store behind an agent that was started where there is none ---

// behind is a lazy store for a folder with nothing in it yet, and an engine
// on top of it, as "holdfast mcp" builds them.
func behind(t *testing.T, d *desk, create bool) (*lazy, *engine.Engine) {
	t.Helper()
	l := &lazy{path: filepath.Join(d.cwd, store.DirName), make: create, init: "holdfast init"}
	t.Cleanup(func() {
		if l.d != nil {
			l.d.Close()
		}
	})
	return l, &engine.Engine{Store: l, World: (&app{}).world(d.cwd)}
}

func TestAStoreNotYetCreatedReadsAsEmpty(t *testing.T) {
	d := sit(t)
	for _, create := range []bool{true, false} {
		l, e := behind(t, d, create)
		if facts, err := l.Facts(); err != nil || len(facts) != 0 {
			t.Errorf("Facts: %v, %v; want none and no error", facts, err)
		}
		if _, err := l.Get("f-abc123"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get: %v, want not found", err)
		}
		if err := l.Retract("f-abc123", fact.Retraction{At: time.Now(), By: "alex", Reason: "wrong"}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Retract: %v, want not found", err)
		}
		if err := l.Vouch("f-abc123", "alex", time.Now()); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Vouch: %v, want not found", err)
		}
		if err := l.Record(nil); err != nil {
			t.Errorf("Record: %v", err)
		}
		// The policy is the one the store will start with, and nothing
		// about it needs saying yet.
		if p, note := l.Policy(); !reflect.DeepEqual(p, policy.Default()) || note != "" {
			t.Errorf("Policy: %+v, %q; want the default and no note", p, note)
		}
		// The engine's questions are answered the same way.
		if listed, err := e.List(true); err != nil || listed.Counts.Total != 0 {
			t.Errorf("List: %+v, %v", listed.Counts, err)
		}
		if got, err := e.Recall(context.Background(), "anything", 0); err != nil || len(got.Facts) != 0 {
			t.Errorf("Recall: %v, %v", got.Facts, err)
		}
		if _, err := e.Get("f-abc123"); err == nil {
			t.Error("Get found a fact in a store that is not there")
		}
	}
	if left := tree(t, d.cwd); len(left) != 0 {
		t.Errorf("looking created %v", left)
	}
}

func TestAStoreToldNotToBeCreatedRefusesTheFirstFact(t *testing.T) {
	d := sit(t)
	l, e := behind(t, d, false)
	_, _, err := e.Remember(context.Background(), engine.Claim{Claim: "Deploys go out on Fridays", By: "scout"})
	var refused *engine.Refused
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "told not to create one") {
		t.Fatalf("Remember: %v, want a refusal that says why", err)
	}
	if left := tree(t, d.cwd); len(left) != 0 {
		t.Fatalf("a refused fact left %v", left)
	}

	// The store is looked for each time, not once.
	d.ok("init")
	v, _, err := e.Remember(context.Background(), engine.Claim{Claim: "Deploys go out on Fridays", By: "scout"})
	if err != nil || v.Status != "unproven" {
		t.Fatalf("Remember after init: %+v, %v", v, err)
	}
	if facts, err := l.Facts(); err != nil || len(facts) != 1 || facts[0].ID != v.ID {
		t.Errorf("Facts after init: %v, %v", facts, err)
	}
	if got := d.why(v.ID); got.By != "scout" {
		t.Errorf("the command line reads %+v", got)
	}
}

// An MCP client may send several requests at once, and each is answered on
// its own: the store must still be created once.
func TestAStoreIsCreatedOnceWhenTheFirstFactsArriveTogether(t *testing.T) {
	d := sit(t)
	d.write("go.mod", goMod)
	l, e := behind(t, d, true)
	const n = 8
	errs := make(chan error, n)
	for i := range n {
		go func() {
			v, _, err := e.Remember(context.Background(), engine.Claim{
				Claim: fmt.Sprintf("The module file exists, said agent %d", i), Check: "file go.mod", By: "scout",
			})
			if err == nil && v.Status != "holds" {
				err = fmt.Errorf("fact %d is %s: %s", i, v.Status, v.Detail)
			}
			errs <- err
		}()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	if facts, err := l.Facts(); err != nil || len(facts) != n {
		t.Errorf("the store holds %d facts (%v), want %d", len(facts), err, n)
	}
	if c := d.listed().Counts; c.Total != n || c.Holds != n {
		t.Errorf("the command line counts %+v, want %d that hold", c, n)
	}
	if !d.inForce() {
		t.Error("the policy of the new store is not in force")
	}
}

// --- serve ---

func reach(addr string) string {
	return "holdfast: " + addr + " can be reached from other machines, and the ledger has no login. Use a 127.0.0.1 address, or pass --expose if that is what you want.\n"
}

// The ledger can retract facts and has no login, so it is opened to other
// machines only by someone who says so in as many words.
func TestServeRefusesAnAddressOtherMachinesCanReach(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "0.0.0.0:7878", ":7878", "[::]:7878", "192.168.1.20:7878", "holdfast.example.com:7878", "localhost.example.com:7878"} {
		t.Run(addr, func(t *testing.T) {
			d := project(t)
			d.add(goClaim, goCheck)
			for _, more := range [][]string{nil, {"--demo"}, {"--global"}, {"--expose=false"}} {
				got := d.run(append([]string{"serve", "--addr", addr}, more...)...)
				if got.code != 2 || got.out != "" || got.err != reach(addr) {
					t.Errorf("serve --addr %s %v: exit %d, %q, %q; want exit 2 and the refusal", addr, more, got.code, got.out, got.err)
				}
			}
		})
	}
}

// These run in a folder with no store, so that an address that is let
// through ends at once with the complaint about the store instead of
// listening.
func TestServeLetsAnAddressOfThisMachineThrough(t *testing.T) {
	d := sit(t)
	for name, args := range map[string][]string{
		"the default":             nil,
		"127.0.0.1":               {"--addr", "127.0.0.1:0"},
		"another loopback number": {"--addr", "127.0.0.2:0"},
		"localhost":               {"--addr", "localhost:0"},
		"localhost in capitals":   {"--addr", "LOCALHOST:0"},
		"IPv6 loopback":           {"--addr", "[::1]:0"},
		"anything, with --expose": {"--addr", "0.0.0.0:0", "--expose"},
	} {
		got := d.run(append([]string{"serve"}, args...)...)
		if got.code != 2 || got.out != "" || !strings.HasPrefix(got.err, "holdfast: there is no Holdfast store here") {
			t.Errorf("%s: exit %d, %q, %q; want the address let through and the store missed", name, got.code, got.out, got.err)
		}
	}
	got := d.run("serve", "now")
	if got.code != 2 || !strings.HasPrefix(got.err, "holdfast: serve takes no arguments\nUsage: holdfast serve [--addr 127.0.0.1:7878] [--demo] [--expose]\n") {
		t.Errorf("serve with an argument: exit %d, %q", got.code, got.err)
	}
}

func TestServeSaysWhenItsAddressIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on this machine: %v", err)
	}
	defer taken.Close()
	addr := taken.Addr().String()

	d := project(t)
	for _, more := range [][]string{nil, {"--demo"}} {
		got := d.run(append([]string{"serve", "--addr", addr}, more...)...)
		if got.code != 2 || got.out != "" || !strings.HasPrefix(got.err, "holdfast: cannot listen on "+addr+": ") {
			t.Errorf("serve %v on a taken address: exit %d, %q, %q", more, got.code, got.out, got.err)
		}
	}
}

var opened = regexp.MustCompile(`^The ledger is open at (http://127\.0\.0\.1:\d+)  \(Ctrl\+C to close it\)\n$`)

// ledger starts "holdfast serve" on a free port of this machine and returns
// its address and the way to close it, which is the way a person does:
// Ctrl+C.
func (d *desk) ledger(args ...string) (url string, closeIt func() (code int, errs string)) {
	d.t.Helper()
	// Whatever the timing, Ctrl+C must not end the tests themselves.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, os.Interrupt)
	d.t.Cleanup(func() { signal.Stop(guard) })

	out, errs := &page{}, &page{}
	a := &app{in: strings.NewReader(""), out: out, err: errs, cwd: d.cwd, env: func(name string) string { return d.env[name] }}
	ended := make(chan int, 1)
	go func() { ended <- a.run(append([]string{"serve", "--addr", "127.0.0.1:0"}, args...)) }()

	var once sync.Once
	code := -1
	closeIt = func() (int, string) {
		once.Do(func() {
			if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
				d.t.Errorf("cannot interrupt: %v", err)
			}
			select {
			case code = <-ended:
			case <-time.After(20 * time.Second):
				d.t.Error("holdfast serve did not close within 20 s of Ctrl+C")
			}
		})
		return code, errs.String()
	}

	for wait := time.Now().Add(20 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		if m := opened.FindStringSubmatch(out.String()); m != nil {
			// The line is printed once the interrupt is being listened for.
			d.t.Cleanup(func() { closeIt() })
			return m[1], closeIt
		}
		select {
		case c := <-ended:
			d.t.Fatalf("holdfast serve ended at once: exit %d, %q, %q", c, out, errs)
		default:
		}
		if time.Now().After(wait) {
			d.t.Fatalf("holdfast serve did not open within 20 s; it printed %q and %q", out, errs)
		}
	}
}

// state is what the ledger's page is given when it opens.
type state struct {
	OK     bool `json:"ok"`
	Result struct {
		Version  string        `json:"version"`
		Demo     bool          `json:"demo"`
		Project  string        `json:"project"`
		Policy   string        `json:"policy"`
		Approved bool          `json:"approved"`
		Facts    []engine.View `json:"facts"`
		Counts   engine.Counts `json:"counts"`
	} `json:"result"`
	Error string `json:"error"`
}

// call asks the ledger the way its page does. A page of another site cannot
// set the header.
func call(t *testing.T, url, method, header string, into any) int {
	t.Helper()
	req, err := http.NewRequest("POST", url+"/api/"+method, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if header != "" {
		req.Header.Set(serve.Header, header)
	}
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("the ledger at %s does not answer: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if into != nil {
		decode(t, string(body), into)
	}
	return resp.StatusCode
}

// without is the note of a binary that has no page to show.
func without() string {
	if web.Built() {
		return ""
	}
	return "holdfast: this binary was built without the web interface; only the API answers. Build with \"make build\".\n"
}

func TestServeOpensTheLedgerOfTheProjectUntilInterrupted(t *testing.T) {
	d := project(t)
	v := d.add(goClaim, goCheck)
	d.env["USER"] = "alex"
	url, closeIt := d.ledger()

	var s state
	if status := call(t, url, "state", "1", &s); status != 200 || !s.OK {
		t.Fatalf("state: %d, %+v", status, s)
	}
	r := s.Result
	if r.Version != version || r.Demo || r.Project != filepath.Base(d.cwd) || !r.Approved || r.Policy != read(t, d.policyFile()) {
		t.Errorf("the ledger describes itself as version %q, demo %v, project %q, approved %v", r.Version, r.Demo, r.Project, r.Approved)
	}
	if len(r.Facts) != 1 || r.Facts[0].ID != v.ID || r.Facts[0].Status != "holds" || r.Counts.Holds != 1 {
		t.Errorf("the ledger shows %+v", r.Facts)
	}
	// A call that does not come from its own page is not answered.
	var refused state
	if status := call(t, url, "state", "", &refused); status != 403 || refused.OK || refused.Error == "" {
		t.Errorf("a call without the page's header: %d, %+v", status, refused)
	}
	// What the command line stores meanwhile shows up: there is one store.
	d.add("Deploys go out on Fridays", "")
	if call(t, url, "state", "1", &s); len(s.Result.Facts) != 2 {
		t.Errorf("after the command line stored a note the ledger shows %d facts", len(s.Result.Facts))
	}
	resp, err := http.Get(url + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if want := map[bool]int{true: 200, false: 404}[web.Built()]; resp.StatusCode != want {
		t.Errorf("the page answers %d, want %d (is the interface built in? %v)", resp.StatusCode, want, web.Built())
	}

	code, errs := closeIt()
	if code != 0 || errs != without() {
		t.Errorf("after Ctrl+C: exit %d, %q; want exit 0 and %q", code, errs, without())
	}
	if conn, err := net.DialTimeout("tcp", strings.TrimPrefix(url, "http://"), time.Second); err == nil {
		conn.Close()
		t.Error("the ledger still listens after it was closed")
	}
}

// The demo is a pretend project: it needs no store and leaves none.
func TestServeDemoShowsAPretendProjectAndTouchesNoStore(t *testing.T) {
	d := sit(t)
	url, closeIt := d.ledger("--demo")
	var s state
	if status := call(t, url, "state", "1", &s); status != 200 || !s.OK {
		t.Fatalf("state: %d, %+v", status, s)
	}
	if !s.Result.Demo || len(s.Result.Facts) == 0 || s.Result.Version != version {
		t.Errorf("the demo describes itself as demo %v with %d facts", s.Result.Demo, len(s.Result.Facts))
	}
	if code, errs := closeIt(); code != 0 || errs != without() {
		t.Errorf("after Ctrl+C: exit %d, %q", code, errs)
	}
	if left := append(tree(t, d.cwd), tree(t, d.home)...); len(left) != 0 {
		t.Errorf("the demo left %v behind", left)
	}
}

// From the home folder the ledger says whose facts these are, not "alex".
func TestServeGlobalCallsTheStoreThisMachine(t *testing.T) {
	d := sit(t)
	d.ok("init", "--global")
	d.add("The router is at 192.168.1.1", "", "--global")
	url, closeIt := d.ledger("--global")
	var s state
	if status := call(t, url, "state", "1", &s); status != 200 || s.Result.Project != "this machine" || len(s.Result.Facts) != 1 {
		t.Errorf("state: %d, project %q, %d facts", status, s.Result.Project, len(s.Result.Facts))
	}
	lacks(t, "the machine's policy as the ledger shows it", s.Result.Policy, "\nfiles ")
	if code, _ := closeIt(); code != 0 {
		t.Errorf("after Ctrl+C: exit %d", code)
	}
}

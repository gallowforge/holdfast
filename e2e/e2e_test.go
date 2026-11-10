//go:build unix

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// bin is the binary under test.
var bin string

func TestMain(m *testing.M) {
	if named := os.Getenv("HOLDFAST_BIN"); named != "" {
		abs, err := filepath.Abs(named)
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2e:", err)
			os.Exit(2)
		}
		bin = abs
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "holdfast-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(2)
	}
	bin = filepath.Join(dir, "holdfast")
	build := exec.Command("go", "build", "-o", bin, "../cmd/holdfast")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: the binary does not build:", err)
		os.Exit(2)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// A project is a throwaway folder with a memory in it, and a home of its own
// so that nothing the tests do touches the real one.
type project struct {
	t    *testing.T
	dir  string
	home string
}

// newProject makes a folder with one file in it and runs "holdfast init".
func newProject(t *testing.T) *project {
	t.Helper()
	base := t.TempDir()
	p := &project{t: t, dir: filepath.Join(base, "app"), home: filepath.Join(base, "home")}
	for _, d := range []string{p.dir, p.home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p.write(".env", "PORT=8080\n")
	p.run(0, "init")
	return p
}

// env is what the binary sees: a home of its own and a fixed author, and
// nothing else of the machine the tests run on.
func (p *project) env() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + p.home,
		"HOLDFAST_STATE=" + filepath.Join(p.home, "state"),
		"HOLDFAST_AGENT=alex",
	}
}

func (p *project) write(name, text string) {
	p.t.Helper()
	path := filepath.Join(p.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

func (p *project) command(args ...string) *exec.Cmd {
	cmd := exec.Command(bin, args...)
	cmd.Dir = p.dir
	cmd.Env = p.env()
	return cmd
}

// run runs one command and returns everything it printed. The exit code has
// to be the one expected: the codes are part of what a script relies on.
func (p *project) run(exit int, args ...string) string {
	p.t.Helper()
	out, err := p.command(args...).CombinedOutput()
	got := 0
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		got = ee.ExitCode()
	case err != nil:
		p.t.Fatalf("holdfast %s: %v", strings.Join(args, " "), err)
	}
	if got != exit {
		p.t.Fatalf("holdfast %s exited %d, want %d. It printed:\n%s", strings.Join(args, " "), got, exit, out)
	}
	return string(out)
}

var anID = regexp.MustCompile(`\bf-[a-z0-9]{6}\b`)

// id finds the id of the fact a command just printed.
func id(t *testing.T, out string) string {
	t.Helper()
	found := anID.FindString(out)
	if found == "" {
		t.Fatalf("no fact id in:\n%s", out)
	}
	return found
}

func has(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("the output does not say %q:\n%s", w, out)
		}
	}
}

// An agent is the far end of the MCP server's pipe.
type agent struct {
	t    *testing.T
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	errs *bytes.Buffer
	next int
}

// serveAgent starts "holdfast mcp" in the project, as the named agent.
func (p *project) serveAgent(as string) *agent {
	p.t.Helper()
	cmd := p.command("mcp", "--as", as)
	in, err := cmd.StdinPipe()
	if err != nil {
		p.t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		p.t.Fatal(err)
	}
	a := &agent{t: p.t, cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<20), errs: &bytes.Buffer{}}
	cmd.Stderr = a.errs
	if err := cmd.Start(); err != nil {
		p.t.Fatal(err)
	}
	p.t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return a
}

// reply is one answer from the server.
type reply struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

func (a *agent) send(msg map[string]any) {
	a.t.Helper()
	msg["jsonrpc"] = "2.0"
	line, err := json.Marshal(msg)
	if err != nil {
		a.t.Fatal(err)
	}
	if _, err := a.in.Write(append(line, '\n')); err != nil {
		a.t.Fatalf("the server stopped reading: %v\n%s", err, a.errs)
	}
}

// ask sends one request and waits for its answer.
func (a *agent) ask(method string, params map[string]any) reply {
	a.t.Helper()
	a.next++
	msg := map[string]any{"id": a.next, "method": method}
	if params != nil {
		msg["params"] = params
	}
	a.send(msg)

	type read struct {
		line []byte
		err  error
	}
	got := make(chan read, 1)
	go func() {
		line, err := a.out.ReadBytes('\n')
		got <- read{line, err}
	}()
	select {
	case r := <-got:
		if r.err != nil {
			a.t.Fatalf("no answer to %s: %v\n%s", method, r.err, a.errs)
		}
		var rep reply
		if err := json.Unmarshal(r.line, &rep); err != nil {
			a.t.Fatalf("the answer to %s is not JSON: %v\n%s", method, err, r.line)
		}
		if rep.ID != a.next {
			a.t.Fatalf("the answer to %s carries id %d, want %d", method, rep.ID, a.next)
		}
		return rep
	case <-time.After(20 * time.Second):
		a.t.Fatalf("no answer to %s within 20 seconds\n%s", method, a.errs)
		return reply{}
	}
}

// hello does the handshake the older revisions of the protocol begin with.
func (a *agent) hello(revision string) {
	a.t.Helper()
	rep := a.ask("initialize", map[string]any{
		"protocolVersion": revision,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "e2e", "version": "1"},
	})
	if rep.Error != nil {
		a.t.Fatalf("initialize was refused: %s", rep.Error.Message)
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(rep.Result, &res); err != nil || res.ProtocolVersion != revision {
		a.t.Fatalf("the server answered in revision %q, want %q", res.ProtocolVersion, revision)
	}
	a.send(map[string]any{"method": "notifications/initialized"})
}

// told is what a tool call gives back: the text an agent reads, and the same
// thing as data.
type told struct {
	Text   string
	Failed bool
	Fact   seen
	Facts  []seen
	Counts map[string]int
}

// seen is the part of a fact these tests look at.
type seen struct {
	ID     string `json:"id"`
	Claim  string `json:"claim"`
	Status string `json:"status"`
	By     string `json:"by"`
}

// call runs one tool. meta is the request's metadata, which the 2026-07-28
// revision uses in place of a handshake; it is nil for the older ones.
func (a *agent) call(meta map[string]any, tool string, args map[string]any) told {
	a.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	params := map[string]any{"name": tool, "arguments": args}
	if meta != nil {
		params["_meta"] = meta
	}
	rep := a.ask("tools/call", params)
	if rep.Error != nil {
		a.t.Fatalf("%s was refused outright: %s", tool, rep.Error.Message)
	}
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
		Data    struct {
			Fact   seen           `json:"fact"`
			Facts  []seen         `json:"facts"`
			Counts map[string]int `json:"counts"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(rep.Result, &res); err != nil {
		a.t.Fatalf("the result of %s does not parse: %v\n%s", tool, err, rep.Result)
	}
	out := told{Failed: res.IsError, Fact: res.Data.Fact, Facts: res.Data.Facts, Counts: res.Data.Counts}
	if len(res.Content) > 0 {
		out.Text = res.Content[0].Text
	}
	return out
}

// bye closes the pipe, which is how a client ends a session, and waits for
// the server to leave cleanly.
func (a *agent) bye() {
	a.t.Helper()
	a.in.Close()
	done := make(chan error, 1)
	go func() { done <- a.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			a.t.Errorf("the server did not leave cleanly when its input closed: %v\n%s", err, a.errs)
		}
	case <-time.After(10 * time.Second):
		a.t.Error("the server was still running 10 seconds after its input closed")
	}
}

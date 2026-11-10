package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/engine"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/sim"
	"github.com/gallowforge/holdfast/internal/store"
	"github.com/gallowforge/holdfast/internal/world"
)

// start is when every test begins: a fixed moment, so nothing depends on the
// day the tests run.
var start = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// wait is how long a test waits for something that should come at once.
const wait = 10 * time.Second

// clock is a clock the test moves by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// machine is the pretend machine the checks run on. Its network can be made
// to hang, which keeps a call in flight for as long as a test needs it there.
type machine struct {
	*sim.World
	hang    atomic.Bool
	started chan struct{} // gets a value when a connection begins to hang
	release chan struct{} // closing it lets the hanging connections go on
}

func (m *machine) Dial(ctx context.Context, addr string, guard world.Guard) error {
	if !m.hang.Load() {
		return m.World.Dial(ctx, addr, guard)
	}
	select {
	case m.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.release:
		return m.World.Dial(ctx, addr, guard)
	}
}

// syncBuffer is a buffer several goroutines may write to.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

const goMod = "module example.com/atlas\n\ngo 1.24\n"

// testPolicy is the default: the project folder and this machine's own network.
const testPolicy = "files .\nnet loopback\n"

// newEngine builds an engine on a pretend machine with a few files and one
// open port, and an empty store that lives in memory.
func newEngine(t testing.TB) (*engine.Engine, *machine, *store.Memory, *clock) {
	t.Helper()
	c := &clock{t: start}
	w := sim.New(c.now)
	w.Write("go.mod", goMod, start.Add(-time.Hour))
	w.Write("config/app.yaml", "port: 8080\nmode: production\n", start.Add(-time.Hour))
	w.Ports["localhost:8080"] = true
	rules, err := policy.Parse(testPolicy)
	if err != nil {
		t.Fatalf("the test's own policy does not parse: %v", err)
	}
	m := &machine{World: w, started: make(chan struct{}, 1), release: make(chan struct{})}
	s := store.NewMemory(rules)
	return &engine.Engine{Store: s, World: m}, m, s, c
}

// session is one client talking to a server over a pair of pipes, the way an
// agent's program talks to "holdfast mcp" over standard input and output.
type session struct {
	t     testing.TB
	srv   *Server
	world *machine
	store *store.Memory
	clock *clock
	log   *syncBuffer

	in    *io.PipeWriter
	lines chan string
	done  chan error
	stop  context.CancelFunc
	id    int
	ended bool
}

// open starts a server and returns the client's end. Every line the server
// ever writes is checked to be one well-formed JSON-RPC answer.
func open(t testing.TB, setup ...func(*Server)) *session {
	t.Helper()
	eng, m, st, c := newEngine(t)
	s := &session{
		t: t, world: m, store: st, clock: c, log: &syncBuffer{},
		lines: make(chan string, 4096), done: make(chan error, 1),
	}
	s.srv = &Server{Engine: eng, Version: "1.2.3-test", Log: s.log}
	for _, fn := range setup {
		fn(s.srv)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s.in = inW
	ctx, stop := context.WithCancel(context.Background())
	s.stop = stop
	go func() {
		err := s.srv.Serve(ctx, inR, outW)
		outW.Close()
		s.done <- err
	}()
	go func() {
		defer close(s.lines)
		r := bufio.NewReaderSize(outR, 64<<10)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				s.lines <- line
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { s.end() })
	return s
}

// rpcFault is the error member of an answer.
type rpcFault struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// reply is one line the server wrote.
type reply struct {
	ID     string // exactly as written, quotes and all
	Result json.RawMessage
	Error  *rpcFault
	Line   string
}

// parseReply fails the test unless line is one JSON-RPC 2.0 answer on one
// line: an object with an id and either a result or an error, nothing else.
func parseReply(t testing.TB, line string) reply {
	t.Helper()
	if !strings.HasSuffix(line, "\n") {
		t.Fatalf("the server wrote a line that does not end in a newline: %q", line)
	}
	body := strings.TrimSuffix(line, "\n")
	if strings.ContainsAny(body, "\r\n") {
		t.Fatalf("an answer is spread over more than one line: %q", line)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatalf("the server wrote something that is not a JSON object: %q (%v)", line, err)
	}
	for name := range fields {
		switch name {
		case "jsonrpc", "id", "result", "error":
		default:
			t.Errorf("an answer has the member %q, which JSON-RPC does not know: %s", name, body)
		}
	}
	if string(fields["jsonrpc"]) != `"2.0"` {
		t.Errorf("an answer does not say jsonrpc 2.0: %s", body)
	}
	id, ok := fields["id"]
	if !ok {
		t.Errorf("an answer has no id: %s", body)
	}
	_, hasResult := fields["result"]
	_, hasError := fields["error"]
	if hasResult == hasError {
		t.Errorf("an answer must have a result or an error, and not both: %s", body)
	}
	r := reply{ID: string(id), Result: fields["result"], Line: body}
	if hasError {
		r.Error = &rpcFault{}
		dec := json.NewDecoder(bytes.NewReader(fields["error"]))
		dec.DisallowUnknownFields()
		if err := dec.Decode(r.Error); err != nil {
			t.Fatalf("the error of an answer is not {code, message, data}: %s (%v)", body, err)
		}
		if r.Error.Code == 0 || r.Error.Message == "" {
			t.Errorf("an error must carry a code and a message: %s", body)
		}
	}
	return r
}

// write sends raw text to the server.
func (s *session) write(text string) {
	s.t.Helper()
	if _, err := io.WriteString(s.in, text); err != nil {
		s.t.Fatalf("cannot write to the server: %v", err)
	}
}

// send writes one message on one line.
func (s *session) send(msg map[string]any) {
	s.t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		s.t.Fatal(err)
	}
	s.write(string(data) + "\n")
}

// next returns the next line the server writes.
func (s *session) next() reply {
	s.t.Helper()
	select {
	case line, ok := <-s.lines:
		if !ok {
			s.t.Fatal("the server closed its output where an answer was expected")
		}
		return parseReply(s.t, line)
	case <-time.After(wait):
		s.t.Fatalf("no answer within %s", wait)
	}
	panic("not reached")
}

// request sends a request under a new id and returns the id.
func (s *session) request(method string, params any) string {
	s.t.Helper()
	s.id++
	msg := map[string]any{"jsonrpc": "2.0", "id": s.id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	s.send(msg)
	return strconv.Itoa(s.id)
}

// notify sends a notification: a message with no id, which gets no answer.
func (s *session) notify(method string, params any) {
	s.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	s.send(msg)
}

// ask sends a request and returns its answer, which must be the very next
// line the server writes.
func (s *session) ask(method string, params any) reply {
	s.t.Helper()
	id := s.request(method, params)
	r := s.next()
	if r.ID != id {
		s.t.Fatalf("asked %s as request %s; the next answer was for %s: %s", method, id, r.ID, r.Line)
	}
	return r
}

// result asks and returns the result, which must not be an error.
func (s *session) result(method string, params any) map[string]any {
	s.t.Helper()
	r := s.ask(method, params)
	if r.Error != nil {
		s.t.Fatalf("%s failed: %d %s", method, r.Error.Code, r.Error.Message)
	}
	var m map[string]any
	if err := json.Unmarshal(r.Result, &m); err != nil || m == nil {
		s.t.Fatalf("the result of %s is not an object: %s", method, r.Result)
	}
	return m
}

// fault asks and returns the error, which must carry the code given.
func (s *session) fault(method string, params any, code int) *rpcFault {
	s.t.Helper()
	r := s.ask(method, params)
	if r.Error == nil {
		s.t.Fatalf("%s was answered with a result, want the error %d: %s", method, code, r.Result)
	}
	if r.Error.Code != code {
		s.t.Errorf("%s failed with %d (%s), want %d", method, r.Error.Code, r.Error.Message, code)
	}
	return r.Error
}

// alive fails the test unless the server still answers.
func (s *session) alive() {
	s.t.Helper()
	if r := s.ask("ping", nil); r.Error != nil || string(r.Result) != "{}" {
		s.t.Fatalf("the server no longer answers a ping properly: %s", r.Line)
	}
}

// end closes the client's side, waits for the server to finish, and returns
// every answer nobody read. A second call returns nothing.
func (s *session) end() []reply {
	s.t.Helper()
	if !s.ended {
		s.ended = true
		s.in.Close()
		select {
		case err := <-s.done:
			if err != nil {
				s.t.Errorf("Serve returned %v when its input ended, want nil", err)
			}
		case <-time.After(wait):
			s.t.Fatalf("Serve did not return within %s of its input ending", wait)
		}
		s.stop()
	}
	var rest []reply
	for line := range s.lines {
		rest = append(rest, parseReply(s.t, line))
	}
	return rest
}

// silence ends the session and fails the test if anything was left unread:
// an answer the test did not expect.
func (s *session) silence() {
	s.t.Helper()
	for _, r := range s.end() {
		s.t.Errorf("the server wrote an answer nobody asked for: %s", r.Line)
	}
}

// outcome is what a tool call came back with.
type outcome struct {
	Text   string
	Failed bool
	// Data is the structured form of the answer; nil when there is none.
	Data map[string]any
	// Whole is the result as sent.
	Whole map[string]any
}

// tool calls a tool the old way: no per-request metadata.
func (s *session) tool(name string, args any) outcome {
	s.t.Helper()
	return s.toolWith(nil, name, args)
}

// toolWith calls a tool with the per-request metadata given.
func (s *session) toolWith(meta map[string]any, name string, args any) outcome {
	s.t.Helper()
	params := map[string]any{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	if meta != nil {
		params["_meta"] = meta
	}
	return readOutcome(s.t, s.result("tools/call", params))
}

func readOutcome(t testing.TB, whole map[string]any) outcome {
	t.Helper()
	o := outcome{Whole: whole}
	content, _ := whole["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("a tool result must carry one block of content, got %v", whole["content"])
	}
	block, _ := content[0].(map[string]any)
	text, isText := block["text"].(string)
	if block["type"] != "text" || !isText || text == "" {
		t.Fatalf("the content of a tool result must be a block of text, got %v", content[0])
	}
	o.Text = text
	switch failed := whole["isError"].(type) {
	case nil:
	case bool:
		o.Failed = failed
	default:
		t.Fatalf("isError must be true or false, got %v", whole["isError"])
	}
	if data, ok := whole["structuredContent"]; ok {
		o.Data, _ = data.(map[string]any)
		if o.Data == nil {
			t.Fatalf("structuredContent must be an object, got %v", data)
		}
	}
	return o
}

// view reads a fact out of a structured answer, in the engine's own shape.
func view(t testing.TB, from any) engine.View {
	t.Helper()
	data, err := json.Marshal(from)
	if err != nil {
		t.Fatal(err)
	}
	var v engine.View
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("this is not a fact as the engine shows one: %s (%v)", data, err)
	}
	if v.ID == "" {
		t.Fatalf("this fact has no id: %s", data)
	}
	return v
}

// views reads the "facts" of a structured answer.
func views(t testing.TB, data map[string]any) []engine.View {
	t.Helper()
	list, ok := data["facts"].([]any)
	if !ok {
		t.Fatalf("the answer's facts must be a list, even an empty one; got %v", data["facts"])
	}
	out := make([]engine.View, len(list))
	for i, item := range list {
		out[i] = view(t, item)
	}
	return out
}

// remember stores a fact through the server; it must be accepted.
func (s *session) remember(claim, check string) engine.View {
	s.t.Helper()
	args := map[string]any{"claim": claim}
	if check != "" {
		args["check"] = check
	}
	o := s.tool("remember", args)
	if o.Failed {
		s.t.Fatalf("remember(%q, %q) was turned down: %s", claim, check, o.Text)
	}
	return view(s.t, o.Data["fact"])
}

// stored returns the facts the store holds, bypassing the server.
func (s *session) stored() []string {
	s.t.Helper()
	all, err := s.store.Facts()
	if err != nil {
		s.t.Fatal(err)
	}
	out := make([]string, len(all))
	for i, f := range all {
		out[i] = f.Claim
	}
	return out
}

// handshake opens a session the old way, as a client with the name given.
func (s *session) handshake(client string) map[string]any {
	s.t.Helper()
	got := s.result("initialize", map[string]any{
		"protocolVersion": LatestLegacy,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": client, "version": "9.9"},
	})
	s.notify("notifications/initialized", nil)
	return got
}

// A client of the older revisions opens with "initialize". The server answers
// in the client's revision when it speaks it, and offers its newest older
// revision otherwise.
func TestInitializeAnswersInTheClientsRevisionWhenItSpeaksIt(t *testing.T) {
	for _, tc := range []struct {
		name, asked, want string
	}{
		{"the newest of the older revisions", "2025-11-25", "2025-11-25"},
		{"2025-06-18", "2025-06-18", "2025-06-18"},
		{"2025-03-26", "2025-03-26", "2025-03-26"},
		{"the first revision", "2024-11-05", "2024-11-05"},
		{"a revision that never existed", "2024-10-07", "2025-11-25"},
		{"a revision from the future", "2031-01-01", "2025-11-25"},
		{"the revision without a handshake", Modern, "2025-11-25"},
		{"not a date", "latest", "2025-11-25"},
		{"nothing", "", "2025-11-25"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			got := s.result("initialize", map[string]any{
				"protocolVersion": tc.asked,
				"capabilities":    map[string]any{},
				"clientInfo":      map[string]any{"name": "claude-code", "version": "2.1"},
			})
			if got["protocolVersion"] != tc.want {
				t.Errorf("asked for %q, the server chose %v, want %s", tc.asked, got["protocolVersion"], tc.want)
			}
		})
	}
}

func TestInitializeSaysWhatTheServerIsAndHowToUseIt(t *testing.T) {
	s := open(t)
	got := s.handshake("claude-code")

	caps, _ := got["capabilities"].(map[string]any)
	if toolsCap, ok := caps["tools"].(map[string]any); !ok || len(caps) != 1 {
		t.Errorf("capabilities = %v, want tools and nothing else", got["capabilities"])
	} else if len(toolsCap) != 0 {
		t.Errorf("the tools capability promises %v; the list of tools never changes, so it must promise nothing", toolsCap)
	}
	info, _ := got["serverInfo"].(map[string]any)
	if info["name"] != "holdfast" || info["title"] != "Holdfast" {
		t.Errorf("serverInfo = %v, want the name holdfast and the title Holdfast", info)
	}
	if info["version"] != "1.2.3-test" {
		t.Errorf("serverInfo.version = %v, want the version the server was given", info["version"])
	}
	if got["instructions"] != Instructions {
		t.Errorf("instructions = %v, want the manual", got["instructions"])
	}
	if _, ok := got["resultType"]; ok {
		t.Error("an answer to a client of an older revision carries resultType, which those revisions do not know")
	}
	for _, word := range []string{"recall", "remember", "retract", "replaces"} {
		if !strings.Contains(Instructions, word) {
			t.Errorf("the manual does not mention %q", word)
		}
	}
}

// With no parameters at all the handshake still succeeds, in the newest of
// the older revisions.
func TestInitializeWithoutParameters(t *testing.T) {
	s := open(t)
	if got := s.result("initialize", nil); got["protocolVersion"] != LatestLegacy {
		t.Errorf("protocolVersion = %v, want %s", got["protocolVersion"], LatestLegacy)
	}
}

func TestPingIsAnsweredWithAnEmptyResult(t *testing.T) {
	s := open(t)
	s.handshake("claude-code")
	if r := s.ask("ping", nil); r.Error != nil || string(r.Result) != "{}" {
		t.Errorf("ping was answered with %s, want an empty result", r.Line)
	}
	// And before any handshake too: a client may ping at any time.
	fresh := open(t)
	if r := fresh.ask("ping", nil); r.Error != nil || string(r.Result) != "{}" {
		t.Errorf("a ping before the handshake was answered with %s, want an empty result", r.Line)
	}
}

// listed asks for the tools and returns them by name, with the names in the
// order the server gave them.
func listed(t testing.TB, got map[string]any) (map[string]map[string]any, []string) {
	t.Helper()
	list, ok := got["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list did not return a list of tools: %v", got)
	}
	byName := map[string]map[string]any{}
	var names []string
	for _, item := range list {
		tl, _ := item.(map[string]any)
		name, _ := tl["name"].(string)
		if name == "" {
			t.Fatalf("a tool has no name: %v", item)
		}
		if byName[name] != nil {
			t.Fatalf("the tool %s is listed twice", name)
		}
		byName[name] = tl
		names = append(names, name)
	}
	return byName, names
}

func TestToolsListNamesExactlyTheFourTools(t *testing.T) {
	s := open(t)
	s.handshake("claude-code")
	got := s.result("tools/list", nil)
	byName, names := listed(t, got)
	if want := []string{"remember", "recall", "verify", "retract"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("the tools are %v, want exactly %v", names, want)
	}
	if len(got) != 1 {
		t.Errorf("the answer to a client of an older revision has more than the tools: %v", keys(got))
	}
	for name, tl := range byName {
		for _, member := range []string{"title", "description"} {
			if text, _ := tl[member].(string); strings.TrimSpace(text) == "" {
				t.Errorf("%s has no %s", name, member)
			}
		}
		if _, ok := tl["inputSchema"].(map[string]any); !ok {
			t.Errorf("%s has no inputSchema", name)
		}
		hints, ok := tl["annotations"].(map[string]any)
		if !ok {
			t.Errorf("%s has no annotations", name)
			continue
		}
		for _, hint := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
			if _, ok := hints[hint].(bool); !ok {
				t.Errorf("%s does not say whether %s", name, hint)
			}
		}
		if len(tl) != 5 {
			t.Errorf("%s has the members %v, want name, title, description, inputSchema and annotations", name, keys(tl))
		}
	}
}

// The hints are what a client goes by when it decides whether to ask a person
// before a call. Nothing here destroys. Nothing is called read-only either:
// recall and verify run checks, and a check may be a command a person allowed.
func TestTheToolsSayHonestlyWhatTheyChange(t *testing.T) {
	s := open(t)
	byName, _ := listed(t, s.result("tools/list", nil))
	for name, readOnly := range map[string]bool{"remember": false, "recall": false, "verify": false, "retract": false} {
		hints, _ := byName[name]["annotations"].(map[string]any)
		if hints["readOnlyHint"] != readOnly {
			t.Errorf("%s: readOnlyHint = %v, want %v", name, hints["readOnlyHint"], readOnly)
		}
		if hints["destructiveHint"] != false {
			t.Errorf("%s: destructiveHint = %v; nothing is ever deleted, so want false", name, hints["destructiveHint"])
		}
	}
	// Retracting only touches the store; the other three run checks against
	// the machine and the network.
	for name, open := range map[string]bool{"remember": true, "recall": true, "verify": true, "retract": false} {
		hints, _ := byName[name]["annotations"].(map[string]any)
		if hints["openWorldHint"] != open {
			t.Errorf("%s: openWorldHint = %v, want %v", name, hints["openWorldHint"], open)
		}
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// sample is a value of the JSON Schema type named, and wrong is a value that
// is certainly not of it.
func sample(kind string) (right, wrong any) {
	switch kind {
	case "string":
		return "x", 7
	case "integer":
		return 1, "seven"
	case "array":
		return []string{"x"}, "one"
	}
	return nil, nil
}

// Every tool describes its arguments with a JSON Schema. A client validates
// against it and a model writes its arguments from it, so it must be a sound
// schema of an object, and it must say what the tool really takes.
func TestEveryToolSchemaIsASoundObjectSchemaThatMatchesTheTool(t *testing.T) {
	s := open(t)
	byName, names := listed(t, s.result("tools/list", nil))
	for _, name := range names {
		schema, _ := byName[name]["inputSchema"].(map[string]any)
		t.Run(name, func(t *testing.T) {
			if schema["type"] != "object" {
				t.Errorf(`"type" = %v, want "object"`, schema["type"])
			}
			if schema["additionalProperties"] != false {
				t.Errorf(`"additionalProperties" = %v, want false: the tool refuses what it does not know`, schema["additionalProperties"])
			}
			for member := range schema {
				switch member {
				case "type", "properties", "required", "additionalProperties":
				default:
					t.Errorf("the schema has the member %q, which this test does not know how to judge", member)
				}
			}
			props, ok := schema["properties"].(map[string]any)
			if !ok || len(props) == 0 {
				t.Fatalf(`"properties" = %v, want an object that names the arguments`, schema["properties"])
			}
			for arg, raw := range props {
				p, ok := raw.(map[string]any)
				if !ok {
					t.Errorf("%s is described by %v, want a schema", arg, raw)
					continue
				}
				kind, _ := p["type"].(string)
				right, wrong := sample(kind)
				if right == nil {
					t.Errorf(`%s: "type" = %v, want string, integer or array`, arg, p["type"])
					continue
				}
				if text, _ := p["description"].(string); strings.TrimSpace(text) == "" {
					t.Errorf("%s has no description; a model has only this to go by", arg)
				}
				for member, value := range p {
					switch member {
					case "type", "description":
					case "items":
						items, _ := value.(map[string]any)
						if kind != "array" || items["type"] != "string" {
							t.Errorf(`%s: "items" = %v on a %s, want strings in an array`, arg, value, kind)
						}
					case "maxItems":
						if n, ok := value.(float64); !ok || kind != "array" || n < 1 || n != float64(int(n)) {
							t.Errorf(`%s: "maxItems" = %v on a %s`, arg, value, kind)
						}
					case "minimum", "maximum":
						if n, ok := value.(float64); !ok || kind != "integer" || n != float64(int(n)) {
							t.Errorf(`%s: %q = %v on a %s`, arg, member, value, kind)
						}
					default:
						t.Errorf("%s: the member %q is one this test does not know how to judge", arg, member)
					}
				}
				if kind == "array" && p["items"] == nil {
					t.Errorf(`%s is an array that does not say what it holds`, arg)
				}
				if lo, hasLo := p["minimum"].(float64); hasLo {
					if hi, hasHi := p["maximum"].(float64); hasHi && lo > hi {
						t.Errorf("%s: no number is at least %v and at most %v", arg, lo, hi)
					}
				}

				// The schema and the tool must agree: what the schema names
				// the tool takes, and a value of another type is turned down.
				if o := s.tool(name, map[string]any{arg: right}); strings.HasPrefix(o.Text, "the arguments are not right") {
					t.Errorf("the schema names %s as a %s, but the tool turns it down: %s", arg, kind, o.Text)
				}
				o := s.tool(name, map[string]any{arg: wrong})
				if !o.Failed || !strings.HasPrefix(o.Text, "the arguments are not right: ") || o.Data != nil {
					t.Errorf("%s = %v (not a %s) was not turned down as a mistake in the arguments: failed %v, %q", arg, wrong, kind, o.Failed, o.Text)
				}
			}

			var required []string
			if raw, ok := schema["required"]; ok {
				list, _ := raw.([]any)
				if len(list) == 0 {
					t.Errorf(`"required" = %v, want a list of names or no such member`, raw)
				}
				for _, item := range list {
					arg, _ := item.(string)
					if _, known := props[arg]; !known {
						t.Errorf(`"required" names %v, which is not among the properties`, item)
					}
					required = append(required, arg)
				}
			}
			// What the schema requires, the tool insists on: with every
			// argument given but that one, the call is turned down.
			for _, missing := range required {
				args := map[string]any{}
				for arg, raw := range props {
					if arg != missing {
						args[arg], _ = sample(raw.(map[string]any)["type"].(string))
					}
				}
				if o := s.tool(name, args); !o.Failed {
					t.Errorf("the schema requires %s, but a call without it went through: %s", missing, o.Text)
				}
			}

			o := s.tool(name, map[string]any{"nope": true})
			if want := `the arguments are not right: unknown field "nope"`; !o.Failed || o.Text != want {
				t.Errorf("an argument the schema does not name was answered with (failed %v) %q, want the error %q", o.Failed, o.Text, want)
			}
		})
	}
}

// The whole of a session as a client of the older revisions has it: the
// handshake, the notification that ends it, then tools.
func TestALegacySessionFromHandshakeToRecall(t *testing.T) {
	s := open(t)
	s.handshake("claude-code")
	s.alive()

	v := s.remember("The API listens on port 8080", "port localhost:8080")
	if v.Status != "holds" || v.By != "claude-code" {
		t.Errorf("the fact came back as %s by %q, want holds by claude-code", v.Status, v.By)
	}
	o := s.tool("recall", map[string]any{"query": "api port"})
	if o.Failed {
		t.Fatalf("recall failed: %s", o.Text)
	}
	got := views(t, o.Data)
	if len(got) != 1 || got[0].ID != v.ID || got[0].Status != "holds" {
		t.Errorf("recall returned %+v, want the one fact, holding", got)
	}
	if !strings.Contains(o.Text, "HOLDS") || !strings.Contains(o.Text, "The API listens on port 8080") {
		t.Errorf("the text of the recall does not show the fact as holding:\n%s", o.Text)
	}
	for _, whole := range []map[string]any{o.Whole, s.result("tools/list", nil), s.result("ping", nil)} {
		if _, ok := whole["resultType"]; ok {
			t.Errorf("an answer to a client of an older revision carries resultType: %v", keys(whole))
		}
	}
	// Four requests were answered above, one by one; the notification got
	// nothing, so nothing is left over.
	s.silence()
}

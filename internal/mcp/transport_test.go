package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
)

// The transport is one JSON message per line. What comes in is whatever the
// program on the other end writes, so every malformed line must be answered
// (or ignored) and leave the server serving.

const pingLine = `{"jsonrpc":"2.0","id":"p","method":"ping"}`

// errorWithoutID reads the next answer, which must be an error with a null id
// and the code and message given.
func (s *session) errorWithoutID(code int, message string) {
	s.t.Helper()
	r := s.next()
	if r.Error == nil || r.ID != "null" {
		s.t.Fatalf("want an error with a null id, got %s", r.Line)
	}
	if r.Error.Code != code || r.Error.Message != message {
		s.t.Errorf("the error is %d %q, want %d %q", r.Error.Code, r.Error.Message, code, message)
	}
}

func TestALineThatIsNotJSONIsAnsweredWithAParseError(t *testing.T) {
	for _, line := range []string{
		`{`,
		`nonsense`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"} {"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{'jsonrpc':'2.0','id':1,'method':'ping'}`,
		"\x00\x01\x02",
		`}`,
	} {
		t.Run(line, func(t *testing.T) {
			s := open(t)
			s.write(line + "\n")
			s.errorWithoutID(-32700, "the message is not valid JSON")
			s.alive()
			s.silence()
		})
	}
}

// Well-formed JSON that is not a request is an invalid request, not a parse
// error. With no id to be read, the answer carries a null id.
func TestJSONThatIsNotARequestIsAnsweredAsAnInvalidRequest(t *testing.T) {
	for _, line := range []string{
		`7`,
		`"ping"`,
		`true`,
		`{"jsonrpc":"2.0","method":1,"params":"bar"}`,
		`{"jsonrpc":2.0,"id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1,"method":["ping"]}`,
	} {
		t.Run(line, func(t *testing.T) {
			s := open(t)
			s.write(line + "\n")
			s.errorWithoutID(-32600, "not a JSON-RPC 2.0 request")
			s.alive()
			s.silence()
		})
	}
}

func TestABatchIsTurnedDownAsAWhole(t *testing.T) {
	for _, line := range []string{
		`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`,
		`[]`,
		`  [1,2,3]`,
		`[{"jsonrpc":"2.0","method":"notifications/initialized"}]`,
		`[`,
	} {
		t.Run(line, func(t *testing.T) {
			s := open(t)
			s.write(line + "\n")
			s.errorWithoutID(-32600, "batches are not supported; send one message per line")
			s.alive()
			// One answer for the batch, and none for what was inside it.
			s.silence()
		})
	}
}

func TestAMessageThatIsNotJSONRPC2IsTurnedDownUnderItsID(t *testing.T) {
	for _, tc := range []struct {
		name, line, id string
	}{
		{"no jsonrpc member", `{"id":4,"method":"ping"}`, `4`},
		{"version 1.0", `{"jsonrpc":"1.0","id":"a","method":"ping"}`, `"a"`},
		{"an empty version", `{"jsonrpc":"","id":5,"method":"ping"}`, `5`},
		{"no method", `{"jsonrpc":"2.0","id":6}`, `6`},
		{"an empty method", `{"jsonrpc":"2.0","id":7,"method":""}`, `7`},
		{"an answer instead of a request", `{"jsonrpc":"2.0","id":8,"result":{}}`, `8`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			s.write(tc.line + "\n")
			r := s.next()
			if r.Error == nil || r.Error.Code != -32600 || r.Error.Message != "not a JSON-RPC 2.0 request" {
				t.Errorf("answered %s, want the error -32600", r.Line)
			}
			if r.ID != tc.id {
				t.Errorf("the answer is for %s, want %s", r.ID, tc.id)
			}
			s.alive()
			s.silence()
		})
	}
}

// A message without an id is a notification, and a notification is never
// answered: not when it is unknown, not when it is malformed, not when it
// names a method that would be answered as a request.
func TestANotificationIsNeverAnswered(t *testing.T) {
	s := open(t)
	for _, line := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"t","progress":1}}`,
		`{"jsonrpc":"2.0","method":"notifications/roots/list_changed"}`,
		`{"jsonrpc":"2.0","method":"notifications/unheard-of"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":12345}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":"nothing"}`,
		`{"jsonrpc":"2.0","method":"ping"}`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
		`{"jsonrpc":"2.0","method":"tools/list"}`,
		`{"jsonrpc":"2.0","method":"no/such/method"}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"no-such-tool"}}`,
		`{"jsonrpc":"1.0","method":"ping"}`,
		`{"method":"ping"}`,
		`{"jsonrpc":"2.0"}`,
		`{}`,
	} {
		s.write(line + "\n")
		// The server reads one line at a time, so had it answered that line,
		// the answer would come before the ping's.
		s.alive()
	}
	s.silence()
}

func TestBlankLinesAreIgnored(t *testing.T) {
	s := open(t)
	s.write("\n\n   \n\t\n\r\n \r\n\xEF\xBB\xBF\n")
	s.alive()
	s.write("\n")
	s.alive()
	s.silence()
}

// Some clients end lines the Windows way, pad them, or start their output
// with a byte order mark. None of that is part of the message.
func TestLineEndingsPaddingAndAByteOrderMarkAreNotPartOfTheMessage(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{"a carriage return before the newline", "", "\r\n"},
		{"spaces around", "   ", "   \n"},
		{"tabs around", "\t", "\t\n"},
		{"a byte order mark", "\xEF\xBB\xBF", "\n"},
		{"a byte order mark and a carriage return", "\xEF\xBB\xBF", "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			s.write(tc.before + pingLine + tc.after)
			if r := s.next(); r.ID != `"p"` || r.Error != nil || string(r.Result) != "{}" {
				t.Errorf("the ping was answered with %s", r.Line)
			}
			s.silence()
		})
	}
}

// The id comes back exactly as it was sent: a number stays that number, to
// the last digit, and text stays text.
func TestTheAnswerCarriesTheIDExactlyAsSent(t *testing.T) {
	s := open(t)
	for _, id := range []string{
		`1`, `0`, `-7`, `"abc"`, `""`, `"7"`, `1.5`, `12345678901234567890123`,
		`"with \"quotes\" and a \\ in it"`, `"request/2026-03-01#4"`, `"null"`,
	} {
		for _, method := range []string{"ping", "no/such/method"} {
			s.write(`{"jsonrpc":"2.0","id":` + id + `,"method":"` + method + `"}` + "\n")
			if r := s.next(); r.ID != id {
				t.Errorf("%s sent as request %s was answered as %s", method, id, r.ID)
			}
		}
	}
	s.silence()
}

// padded returns a ping that is exactly size bytes long, the newline not counted.
func padded(size int) string {
	const head, tail = `{"jsonrpc":"2.0","id":"big","method":"ping","params":{"pad":"`, `"}}`
	return head + strings.Repeat("a", size-len(head)-len(tail)) + tail
}

func TestALineOfExactlyTheLimitIsStillServed(t *testing.T) {
	s := open(t)
	line := padded(maxLine)
	if len(line) != maxLine {
		t.Fatalf("the test's own line is %d bytes, not the limit", len(line))
	}
	s.write(line + "\n")
	if r := s.next(); r.ID != `"big"` || r.Error != nil || string(r.Result) != "{}" {
		t.Errorf("a ping of exactly %d bytes was answered with %.200s", maxLine, r.Line)
	}
	s.silence()
}

// A line over the limit is not kept in memory: it is skipped whole, answered
// once, and whatever follows it is served as usual.
func TestALineThatIsTooLongIsAnsweredOnceAndSkipped(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
	}{
		{"one byte over", padded(maxLine + 1)},
		{"more than twice the limit", padded(2*maxLine + 4096)},
		{"long and not even JSON", strings.Repeat("x", maxLine+70_000)},
		{"long and full of requests", strings.Repeat(pingLine+" ", maxLine/len(pingLine)+2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			s.alive()
			s.write(tc.line + "\n")
			s.errorWithoutID(-32600, "the message is too long")
			s.alive()
			s.write(tc.line + "\r\n" + pingLine + "\n")
			s.errorWithoutID(-32600, "the message is too long")
			if r := s.next(); r.ID != `"p"` || r.Error != nil {
				t.Errorf("the line after the long one was answered with %.200s", r.Line)
			}
			s.silence()
		})
	}
}

// The input may end in the middle of a line that is too long.
func TestALineThatIsTooLongAtTheVeryEnd(t *testing.T) {
	s := open(t)
	s.write(strings.Repeat("x", maxLine+1))
	rest := s.end()
	if len(rest) != 1 || rest[0].Error == nil || rest[0].Error.Code != -32600 || rest[0].Error.Message != "the message is too long" {
		t.Errorf("the server answered %v, want the one error for the long line", rest)
	}
}

func TestTheLastLineNeedsNoNewline(t *testing.T) {
	s := open(t)
	s.write(pingLine)
	rest := s.end()
	if len(rest) != 1 || rest[0].ID != `"p"` || rest[0].Error != nil {
		t.Errorf("a last line without a newline was answered with %v, want the one answer to the ping", rest)
	}
}

func TestServeReturnsNilWhenItsInputEnds(t *testing.T) {
	eng, _, _, _ := newEngine(t)
	for _, input := range []string{"", "\n", pingLine + "\n", pingLine, "garbage\n", "[]"} {
		var out bytes.Buffer
		srv := &Server{Engine: eng, Version: "test"}
		if err := srv.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
			t.Errorf("Serve(%q) = %v, want nil", input, err)
		}
		want := 1
		if strings.TrimSpace(input) == "" {
			want = 0
		}
		if got := strings.Count(out.String(), "\n"); got != want {
			t.Errorf("Serve(%q) wrote %d lines, want %d: %q", input, got, want, out.String())
		}
	}
}

func TestServeReportsAReadErrorThatIsNotTheEnd(t *testing.T) {
	eng, _, _, _ := newEngine(t)
	boom := errors.New("the pipe broke")
	var out bytes.Buffer
	srv := &Server{Engine: eng, Version: "test"}
	in := io.MultiReader(strings.NewReader(pingLine+"\n"), iotest.ErrReader(boom))
	if err := srv.Serve(context.Background(), in, &out); !errors.Is(err, boom) {
		t.Errorf("Serve = %v, want the error of the reader", err)
	}
	// What was read before the error was served.
	if r := parseReply(t, out.String()); r.ID != `"p"` || r.Error != nil {
		t.Errorf("the ping before the error was answered with %s", r.Line)
	}
}

// Serve ends when its context is cancelled, also while it waits for a line
// that never comes: that is how "holdfast mcp" stops on an interrupt.
func TestServeStopsWhenItsContextIsCancelledEvenWithNothingToRead(t *testing.T) {
	eng, _, _, _ := newEngine(t)
	in, client := io.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	out := &syncBuffer{}
	go func() { done <- (&Server{Engine: eng, Version: "test"}).Serve(ctx, in, out) }()

	// One answered request shows the server is up and reading.
	if _, err := io.WriteString(client, pingLine+"\n"); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Serve = %v, want the context's own error", err)
		}
	case <-time.After(wait):
		t.Fatalf("Serve was still waiting for input %s after its context was cancelled", wait)
	}
}

// cancellable is a tool call that runs a check over the network, which the
// tests below keep hanging.
var cancellable = []struct {
	tool string
	args string
}{
	{"remember", `{"claim":"The cache listens on port 8080","check":"port localhost:8080"}`},
	{"recall", `{"query":"api port"}`},
	{"verify", `{}`},
}

// hung starts a session with one proved fact in it and a network that has
// just stopped answering, and sends the tool call given as request id.
func hung(t *testing.T, id, tool, args string) (*session, fact.Fact) {
	t.Helper()
	s := open(t)
	v := s.remember("The API listens on port 8080", "port localhost:8080")
	before, err := s.store.Get(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Evidence older than half a minute is not taken on trust, so the recall
	// below has to run the check.
	s.clock.add(time.Minute)
	s.world.hang.Store(true)
	s.write(`{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}` + "\n")
	select {
	case <-s.world.started:
	case <-time.After(wait):
		t.Fatalf("the %s never got as far as its check", tool)
	}
	return s, *before
}

// unchanged fails the test unless the store holds exactly the one fact it
// held before, with the evidence it had.
func (s *session) unchanged(before fact.Fact) {
	s.t.Helper()
	all, err := s.store.Facts()
	if err != nil || len(all) != 1 {
		s.t.Fatalf("the store holds %d facts (%v), want the one from before", len(all), err)
	}
	now := all[0]
	if now.ID != before.ID || now.Last == nil || before.Last == nil || *now.Last != *before.Last || len(now.History) != len(before.History) {
		s.t.Errorf("the fact changed: its evidence is %+v, was %+v", now.Last, before.Last)
	}
}

// A client that no longer wants an answer says so, and the server stops the
// work and sends none. Nothing half-done is written down as a result.
func TestACancelledCallIsStoppedAndSendsNoAnswer(t *testing.T) {
	for _, tc := range cancellable {
		for _, id := range []string{`41`, `"call-41"`} {
			t.Run(tc.tool+" as request "+id, func(t *testing.T) {
				s, before := hung(t, id, tc.tool, tc.args)

				// The call is in flight, and the server answers others meanwhile.
				s.alive()
				s.write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":` + id + `,"reason":"the person pressed stop"}}` + "\n")

				// The server waits for its calls before it returns, so once
				// it has ended, everything it was ever going to write is here.
				for _, r := range s.end() {
					if r.ID == id {
						t.Errorf("the cancelled call was answered all the same: %s", r.Line)
					} else {
						t.Errorf("an answer nobody asked for: %s", r.Line)
					}
				}
				s.unchanged(before)
				if log := s.log.String(); log != "" {
					t.Errorf("a cancelled %s was logged as a problem: %q", tc.tool, log)
				}
			})
		}
	}
}

// After a cancellation the server is as it was: the next call is served, and
// the same id may be used again.
func TestTheServerKeepsServingAfterACancellation(t *testing.T) {
	s, _ := hung(t, `41`, "verify", `{}`)
	s.notify("notifications/cancelled", map[string]any{"requestId": 41})
	s.world.hang.Store(false)
	s.alive()

	s.write(`{"jsonrpc":"2.0","id":41,"method":"tools/call","params":{"name":"verify"}}` + "\n")
	r := s.next()
	if r.ID != "41" || r.Error != nil || !strings.Contains(string(r.Result), "Proved again just now.") {
		t.Errorf("a verify under the id of the cancelled one was answered with %s", r.Line)
	}
	s.silence()
}

// A cancellation stops the call it names and no other.
func TestACancellationThatNamesAnotherRequestStopsNothing(t *testing.T) {
	for _, other := range []string{`42`, `"41"`, `null`, `[41]`} {
		t.Run(other, func(t *testing.T) {
			s, _ := hung(t, `41`, "verify", `{}`)
			s.write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":` + other + `}}` + "\n")
			s.alive()
			close(s.world.release)
			r := s.next()
			if r.ID != "41" || r.Error != nil || !strings.Contains(string(r.Result), "Proved again just now.") {
				t.Errorf("the verify was answered with %s, want its full answer", r.Line)
			}
			s.silence()
		})
	}
}

// When the input ends, calls still in flight are finished and answered before
// Serve returns: a client that closes its end right after asking still gets
// its answer.
func TestServeAnswersWhatIsInFlightBeforeItReturns(t *testing.T) {
	s, _ := hung(t, `41`, "verify", `{}`)
	s.in.Close()
	s.ended = true
	select {
	case err := <-s.done:
		t.Fatalf("Serve returned (%v) with a call still in flight", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(s.world.release)
	r := s.next()
	if r.ID != "41" || r.Error != nil || !strings.Contains(string(r.Result), "Proved again just now.") {
		t.Errorf("the call in flight was answered with %s", r.Line)
	}
	select {
	case err := <-s.done:
		if err != nil {
			t.Errorf("Serve = %v, want nil", err)
		}
	case <-time.After(wait):
		t.Fatal("Serve did not return after its last call was answered")
	}
	s.silence()
}

// When the server itself is told to stop, calls in flight are stopped with it
// and nothing is written for them.
func TestStoppingTheServerStopsItsCallsWithoutAnAnswer(t *testing.T) {
	for _, tc := range cancellable {
		t.Run(tc.tool, func(t *testing.T) {
			s, before := hung(t, `41`, tc.tool, tc.args)
			s.stop()
			select {
			case err := <-s.done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("Serve = %v, want the context's own error", err)
				}
			case <-time.After(wait):
				t.Fatal("Serve did not return after it was told to stop")
			}
			s.ended = true
			s.in.Close()
			s.silence()
			s.unchanged(before)
		})
	}
}

// Requests are served side by side, and every one gets exactly one answer,
// under its own id, whole on its own line.
func TestManyRequestsAtOnceEachGetExactlyOneAnswer(t *testing.T) {
	s := open(t)
	s.remember("The API listens on port 8080", "port localhost:8080")
	s.remember("The module is example.com/atlas", `contains go.mod "module example.com/atlas"`)

	const n = 240
	want := map[string]string{}
	var batch strings.Builder
	for i := range n {
		id := fmt.Sprintf(`"r%d"`, i)
		switch i % 4 {
		case 0:
			want[id] = "ping"
			fmt.Fprintf(&batch, `{"jsonrpc":"2.0","id":%s,"method":"ping"}`+"\n", id)
		case 1:
			want[id] = "tools/list"
			fmt.Fprintf(&batch, `{"jsonrpc":"2.0","id":%s,"method":"tools/list"}`+"\n", id)
		case 2:
			want[id] = "recall"
			fmt.Fprintf(&batch, `{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"recall","arguments":{"query":"port"}}}`+"\n", id)
		case 3:
			want[id] = "verify"
			fmt.Fprintf(&batch, `{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"verify"}}`+"\n", id)
		}
	}
	s.write(batch.String())
	for range n {
		r := s.next()
		kind, ok := want[r.ID]
		if !ok {
			t.Fatalf("an answer for %s, which was never asked or was answered already: %.200s", r.ID, r.Line)
		}
		delete(want, r.ID)
		if r.Error != nil {
			t.Errorf("%s (%s) failed: %s", r.ID, kind, r.Error.Message)
			continue
		}
		marks := map[string]string{"ping": "{}", "tools/list": `"inputSchema"`, "recall": "The API listens on port 8080", "verify": "Proved again just now."}
		if !strings.Contains(string(r.Result), marks[kind]) {
			t.Errorf("%s was a %s, but its answer is %.200s", r.ID, kind, r.Result)
		}
	}
	s.silence()
}

// FuzzServe feeds the server whatever bytes a client might write. Whatever
// comes in, the server must not crash, must end cleanly when the input does,
// and must write nothing but well-formed answers, at most one per line read.
func FuzzServe(f *testing.F) {
	for _, seed := range []string{
		"",
		"\n\n\n",
		pingLine + "\n",
		pingLine,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"2.1"}}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"remember","arguments":{"claim":"The API listens on port 8080","check":"port localhost:8080","tags":["api"]}}}` + "\n" +
			`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"recall","arguments":{"query":"api port","limit":3}}}` + "\n" +
			`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"verify","arguments":{"ids":["f-abc234"]}}}` + "\n" +
			`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"retract","arguments":{"id":"f-abc234","reason":"gone"}}}` + "\n",
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"cursor"}},"name":"remember","arguments":{"claim":"A note","ttl":"7d"}}}` + "\n",
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1999-01-01"}}}` + "\n",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"remember","arguments":{"claim":"x","check":"not file legacy/config.yml"}}}` + "\n",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"remember","arguments":{"claim":"x","check":"cmd make deploy"}}}` + "\n",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"remember","arguments":{"claim":"x","check":"http 200 http://localhost:8080/healthz"}}}` + "\n",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall","arguments":null}}` + "\n",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall","arguments":[1,2]}}` + "\n",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall","arguments":{"limit":-5,"query":"\u0000\ud83d"}}}` + "\n",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":[]}` + "\n",
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}` + "\n",
		`{"jsonrpc":"2.0","id":{"a":[1,2]},"method":"ping"}` + "\n",
		`{"jsonrpc":"2.0","id":[],"method":"no/such"}` + "\n",
		`{"jsonrpc":"2.0","id":1e400,"method":"ping"}` + "\n",
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":null}` + "\n",
		`{"jsonrpc":"1.0","id":1,"method":"ping"}` + "\n",
		`[{"jsonrpc":"2.0","id":1,"method":"ping"}]` + "\n",
		"[\n]\n{\n}\n",
		"\xEF\xBB\xBF" + pingLine + "\r\n",
		"\xEF\xBB\xBF\xEF\xBB\xBF \t\r\n",
		"\xff\xfe\x00{\x00}\n",
		`{"jsonrpc":"2.0","id":"` + strings.Repeat("9", 5000) + `","method":"ping"}` + "\n",
		strings.Repeat(`{"a":`, 2000) + "\n",
		strings.Repeat("[", 20000) + "\n" + pingLine + "\n",
		`"just text"` + "\n7\nnull\ntrue\n",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"remember","arguments":{"claim":"` + strings.Repeat("long ", 400) + `"}}}` + "\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		eng, _, _, _ := newEngine(t)
		var out bytes.Buffer
		log := &syncBuffer{}
		srv := &Server{Engine: eng, Version: "fuzz", Log: log}
		if err := srv.Serve(context.Background(), bytes.NewReader(input), &out); err != nil {
			t.Fatalf("Serve = %v, want nil at the end of the input", err)
		}
		answers := 0
		for line := range strings.SplitAfterSeq(out.String(), "\n") {
			if line == "" {
				continue
			}
			parseReply(t, line)
			answers++
		}
		if most := bytes.Count(input, []byte("\n")) + 1; answers > most {
			t.Errorf("%d answers to at most %d messages", answers, most)
		}
		if strings.Contains(log.String(), "panic") {
			t.Errorf("something panicked: %s", log.String())
		}
	})
}

// wantLines is what a line reader should make of data: the lines without
// their newline, and without the carriage return some clients put before it.
func wantLines(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			out = append(out, data)
			break
		}
		out = append(out, bytes.TrimSuffix(data[:i], []byte("\r")))
		data = data[i+1:]
	}
	return out
}

// FuzzReadLine checks the line reader against the plain definition of a line,
// with a buffer so small that most lines arrive in several pieces.
func FuzzReadLine(f *testing.F) {
	for _, seed := range []string{
		"", "\n", "a", "a\n", "a\nb", "a\r\nb\r\n", "\r", "\r\r\n", "\n\n\n",
		"exactly sixteen!\n", "fifteen bytes..\r\nnext", "fifteen bytes..\rnot the end\n",
		"fifteen bytes..\r", strings.Repeat("0123456789abcdef", 9) + "\r\n" + strings.Repeat("x", 33),
		pingLine + "\n" + pingLine + "\r\n" + pingLine,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r := bufio.NewReaderSize(bytes.NewReader(data), 16)
		var got [][]byte
		for {
			line, err := readLine(r)
			if err != nil {
				if err != io.EOF {
					t.Fatalf("readLine = %v", err)
				}
				if len(line) > 0 {
					got = append(got, line)
				}
				break
			}
			got = append(got, line)
			if len(got) > len(data)+1 {
				t.Fatal("readLine returns more lines than there are bytes")
			}
		}
		want := wantLines(data)
		if len(got) != len(want) {
			t.Fatalf("%q was read as %d lines, want %d: %q", data, len(got), len(want), got)
		}
		for i := range want {
			if !bytes.Equal(got[i], want[i]) {
				t.Errorf("line %d of %q was read as %q, want %q", i, data, got[i], want[i])
			}
		}
	})
}

package mcp

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/engine"
	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/sim"
	"github.com/gallowforge/holdfast/internal/store"
)

// sameAsText fails the test unless the text of a tool answer is the heading
// given, a blank line, and then the fact exactly as the engine words it: the
// text an agent reads and the structured answer must say the same thing.
func sameAsText(t *testing.T, o outcome, head string, v engine.View) {
	t.Helper()
	if want := head + "\n\n" + v.Text(start); o.Text != want {
		t.Errorf("the text is\n%s\nwant\n%s", o.Text, want)
	}
}

func TestRememberStoresAFactAndSaysItWasProved(t *testing.T) {
	s := open(t)
	s.handshake("claude-code")
	o := s.tool("remember", map[string]any{
		"claim": "The module is example.com/atlas",
		"check": `contains go.mod "module example.com/atlas"`,
		"tags":  []string{"build", "go"},
	})
	if o.Failed {
		t.Fatalf("remember was turned down: %s", o.Text)
	}
	v := view(t, o.Data["fact"])
	sameAsText(t, o, "Stored, and proved just now.", v)
	if !fact.ValidID(v.ID) {
		t.Errorf("the id %q is not a fact id", v.ID)
	}
	if v.Status != fact.Holds || !v.Fresh || v.By != "claude-code" || !reflect.DeepEqual(v.Tags, []string{"build", "go"}) {
		t.Errorf("the fact is %s (fresh %v) by %q with tags %v, want holds, fresh, by claude-code, tagged build and go", v.Status, v.Fresh, v.By, v.Tags)
	}
	if o.Data["already"] != false {
		t.Errorf(`"already" = %v on a fact stored for the first time`, o.Data["already"])
	}
	if got := s.stored(); !reflect.DeepEqual(got, []string{"The module is example.com/atlas"}) {
		t.Errorf("the store holds %v", got)
	}
}

func TestRememberTwiceStoresOnce(t *testing.T) {
	s := open(t)
	first := s.remember("The API listens on port 8080", "port localhost:8080")
	o := s.tool("remember", map[string]any{"claim": "The API listens on port 8080", "check": "port localhost:8080"})
	if o.Failed {
		t.Fatalf("remembering the same fact again was turned down: %s", o.Text)
	}
	v := view(t, o.Data["fact"])
	sameAsText(t, o, "This was already remembered; nothing new was stored.", v)
	if v.ID != first.ID || o.Data["already"] != true {
		t.Errorf("got %s (already %v), want the first fact %s again", v.ID, o.Data["already"], first.ID)
	}
	if got := s.stored(); len(got) != 1 {
		t.Errorf("the store holds %d facts, want 1: %v", len(got), got)
	}
}

// A turned-down call is an answer for the agent to read and act on: a tool
// result marked as an error, with the reason as text and no structured data.
func TestRememberTurnsDownWhatIsNotAFact(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want string // the start of the text
	}{
		{"a check that fails right now",
			map[string]any{"claim": "The API listens on port 9999", "check": "port localhost:9999"},
			"the check fails right now, so this is not a fact yet: "},
		{"a file that is not there",
			map[string]any{"claim": "There is a Makefile", "check": "file Makefile"},
			"the check fails right now, so this is not a fact yet: "},
		{"no claim", map[string]any{"check": "file go.mod"}, ""},
		{"an empty claim", map[string]any{"claim": "   ", "check": "file go.mod"}, ""},
		{"a check in no known language", map[string]any{"claim": "The API is up", "check": "is it up"}, ""},
		{"a time to live that is not a duration", map[string]any{"claim": "The release is on Friday", "ttl": "soon"}, ""},
		{"an id to replace that is not an id", map[string]any{"claim": "The API listens on 8080", "replaces": "the old one"},
			`"the old one" is not a fact id; ids look like f-abc234`},
		{"a claim that is not text", map[string]any{"claim": 8080}, `the arguments are not right: "claim" is the wrong kind of value (number)`},
		{"tags that are not a list", map[string]any{"claim": "The API is up", "tags": "api"}, `the arguments are not right: "tags" is the wrong kind of value (string)`},
		// Which tag it was is named or not, depending on the Go release.
		{"a tag that is not text", map[string]any{"claim": "The API is up", "tags": []any{"api", 7}}, `the arguments are not right: "tags`},
		{"an argument the tool does not have", map[string]any{"claim": "The API is up", "proof": "port localhost:8080"},
			`the arguments are not right: unknown field "proof"`},
		{"arguments that are not an object", nil, "the arguments are not right: expected an object, got array"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			var args any = tc.args
			if tc.args == nil {
				args = []string{"claim", "The API is up"}
			}
			o := s.tool("remember", args)
			if !o.Failed {
				t.Fatalf("this was stored: %s", o.Text)
			}
			if !strings.HasPrefix(o.Text, tc.want) {
				t.Errorf("the reason is %q, want it to begin %q", o.Text, tc.want)
			}
			// The agent is told in plain words, not in the words of the
			// language Holdfast is written in.
			for _, word := range []string{"unmarshal", "Go ", "struct", "json:"} {
				if strings.Contains(o.Text, word) {
					t.Errorf("the reason %q speaks of %q, which means nothing to an agent", o.Text, word)
				}
			}
			if o.Data != nil {
				t.Errorf("a turned-down call carries structured data: %v", o.Data)
			}
			if got := s.stored(); len(got) != 0 {
				t.Errorf("something was stored all the same: %v", got)
			}
			if _, ok := o.Whole["isError"].(bool); !ok {
				t.Errorf("isError = %v, want true", o.Whole["isError"])
			}
			s.alive()
		})
	}
}

func TestRememberSaysWhenACheckMayNotRun(t *testing.T) {
	for _, check := range []string{
		"http 200 https://example.com/health",
		"port db.internal:5432",
		"file /etc/hosts",
		"cmd make test",
	} {
		t.Run(check, func(t *testing.T) {
			s := open(t)
			o := s.tool("remember", map[string]any{"claim": "Something beyond the project", "check": check})
			if o.Failed {
				t.Fatalf("a check the policy does not allow must be stored as blocked, not turned down: %s", o.Text)
			}
			v := view(t, o.Data["fact"])
			sameAsText(t, o, "Stored, but not proved: the check may not run until a person allows it. Tell the person; until then treat this as unproven.", v)
			if v.Status != fact.Blocked || v.Detail == "" {
				t.Errorf("the fact is %s (%q), want blocked with the reason", v.Status, v.Detail)
			}
		})
	}
}

func TestRememberSaysWhenANoteWillGoStale(t *testing.T) {
	for _, tc := range []struct {
		ttl, old string
		life     time.Duration
	}{
		{"", "30d", 30 * 24 * time.Hour},
		{"12h", "12h", 12 * time.Hour},
		{"4w", "4w", 28 * 24 * time.Hour},
		{"90m", "90m", 90 * time.Minute},
	} {
		t.Run("ttl "+tc.ttl, func(t *testing.T) {
			s := open(t)
			args := map[string]any{"claim": "The release branch is cut on Thursdays"}
			if tc.ttl != "" {
				args["ttl"] = tc.ttl
			}
			o := s.tool("remember", args)
			if o.Failed {
				t.Fatalf("a note was turned down: %s", o.Text)
			}
			v := view(t, o.Data["fact"])
			sameAsText(t, o, "Stored as a note. It has no check, so nothing proves it and it will be marked stale when it is "+
				tc.old+" old. Give facts a check whenever one is possible.", v)
			if v.Status != fact.Unproven || v.StaleAt == nil || !v.StaleAt.Equal(start.Add(tc.life)) {
				t.Errorf("the note is %s and goes stale at %v, want unproven until %v", v.Status, v.StaleAt, start.Add(tc.life))
			}
		})
	}
}

func TestRememberWithReplacesWithdrawsTheOlderFact(t *testing.T) {
	s := open(t)
	old := s.remember("The API listens on port 8080", "port localhost:8080")
	s.world.Change(func(w *sim.World) {
		delete(w.Ports, "localhost:8080")
		w.Ports["localhost:9090"] = true
	})
	o := s.tool("remember", map[string]any{
		"claim": "The API listens on port 9090", "check": "port localhost:9090", "replaces": old.ID,
	})
	if o.Failed {
		t.Fatalf("the replacement was turned down: %s", o.Text)
	}
	v := view(t, o.Data["fact"])
	sameAsText(t, o, "Stored, and proved just now. "+old.ID+" was withdrawn in its favour.", v)
	if v.Replaces != old.ID {
		t.Errorf("the new fact replaces %q, want %s", v.Replaces, old.ID)
	}
	before, err := s.store.Get(old.ID)
	if err != nil || before.ReplacedBy != v.ID || before.Live() {
		t.Errorf("the older fact is live %v and replaced by %q (%v), want it withdrawn in favour of %s", before.Live(), before.ReplacedBy, err, v.ID)
	}
	got := views(t, s.tool("recall", nil).Data)
	if len(got) != 1 || got[0].ID != v.ID {
		t.Errorf("recall returns %+v, want only the new fact", got)
	}
}

func TestRecallOfAnEmptyStoreIsAnAnswerNotAnError(t *testing.T) {
	s := open(t)
	// With an empty object, with null, and with no arguments at all.
	for _, params := range []map[string]any{
		{"name": "recall", "arguments": map[string]any{}},
		{"name": "recall", "arguments": nil},
		{"name": "recall"},
	} {
		o := readOutcome(t, s.result("tools/call", params))
		if o.Failed || o.Text != "Nothing is remembered here yet." {
			t.Errorf("recall with %v answered (failed %v) %q", params, o.Failed, o.Text)
		}
		if got := views(t, o.Data); len(got) != 0 {
			t.Errorf("an empty store returned %d facts", len(got))
		}
		if o.Data["known"] != float64(0) || o.Data["matched"] != float64(0) {
			t.Errorf("known %v and matched %v, want 0 and 0", o.Data["known"], o.Data["matched"])
		}
	}
}

func TestRecallReturnsWhatMatchesProvedAgain(t *testing.T) {
	s := open(t)
	port := s.remember("The API listens on port 8080", "port localhost:8080")
	mod := s.remember("The module is example.com/atlas", `contains go.mod "module example.com/atlas"`)
	note := s.remember("The release branch is cut on Thursdays", "")

	o := s.tool("recall", map[string]any{"query": "which port does the api use"})
	if got := views(t, o.Data); o.Failed || len(got) == 0 || got[0].ID != port.ID {
		t.Fatalf("recall did not put the fact about the port first: %+v (%s)", got, o.Text)
	}
	if want := (engine.Recalled{Facts: views(t, o.Data), Matched: int(o.Data["matched"].(float64)), Known: 3}); o.Text != want.Text(start) {
		t.Errorf("the text and the structured answer disagree:\n%s\nwant\n%s", o.Text, want.Text(start))
	}
	if o.Data["known"] != float64(3) {
		t.Errorf("known = %v, want 3", o.Data["known"])
	}

	all := views(t, s.tool("recall", nil).Data)
	if len(all) != 3 {
		t.Fatalf("recall with no query returned %d facts, want all 3", len(all))
	}
	one := s.tool("recall", map[string]any{"limit": 1})
	if got := views(t, one.Data); len(got) != 1 || one.Data["matched"] != float64(3) {
		t.Errorf("recall with a limit of 1 returned %d facts of %v that match, want 1 of 3", len(got), one.Data["matched"])
	}

	// The world changes behind the store's back. The next recall finds out.
	s.world.Change(func(w *sim.World) { delete(w.Ports, "localhost:8080") })
	s.clock.add(time.Minute)
	o = s.tool("recall", map[string]any{"query": "api port"})
	got := views(t, o.Data)
	if o.Failed || len(got) == 0 || got[0].ID != port.ID || got[0].Status != fact.Broken {
		t.Fatalf("after the port closed, recall returned %+v, want the fact as broken", got)
	}
	if !strings.Contains(o.Text, "BROKEN") {
		t.Errorf("the text does not say BROKEN:\n%s", o.Text)
	}
	_, _ = mod, note
}

func TestRecallTurnsDownArgumentsItDoesNotKnow(t *testing.T) {
	s := open(t)
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"limit": "many"}, `the arguments are not right: "limit" is the wrong kind of value (string)`},
		{map[string]any{"query": []string{"api"}}, `the arguments are not right: "query" is the wrong kind of value (array)`},
		{map[string]any{"q": "api"}, `the arguments are not right: unknown field "q"`},
		{map[string]any{"limit": 1.5}, `the arguments are not right: "limit" is the wrong kind of value (number 1.5)`},
	} {
		o := s.tool("recall", tc.args)
		if !o.Failed || o.Text != tc.want || o.Data != nil {
			t.Errorf("recall with %v answered (failed %v) %q, want the error %q", tc.args, o.Failed, o.Text, tc.want)
		}
	}
}

func TestVerifyProvesEverythingOrTheFactsNamed(t *testing.T) {
	s := open(t)
	o := s.tool("verify", nil)
	if o.Failed || o.Text != "There are no facts with checks to prove." || len(views(t, o.Data)) != 0 {
		t.Fatalf("verify of an empty store answered (failed %v) %q with %v", o.Failed, o.Text, o.Data["facts"])
	}

	port := s.remember("The API listens on port 8080", "port localhost:8080")
	mod := s.remember("The module is example.com/atlas", `contains go.mod "module example.com/atlas"`)
	s.remember("The release branch is cut on Thursdays", "")
	s.world.Change(func(w *sim.World) { delete(w.Ports, "localhost:8080") })

	o = s.tool("verify", map[string]any{})
	got := views(t, o.Data)
	if o.Failed || len(got) != 2 {
		t.Fatalf("verify returned %d facts, want the 2 that have a check: %s", len(got), o.Text)
	}
	counts, _ := o.Data["counts"].(map[string]any)
	if counts["total"] != float64(2) || counts["broken"] != float64(1) || counts["holds"] != float64(1) {
		t.Errorf("counts = %v, want 2 in all: 1 broken, 1 holding", counts)
	}
	if got[0].ID != port.ID || got[0].Status != fact.Broken {
		t.Errorf("the first fact is %s (%s), want the broken one first", got[0].Claim, got[0].Status)
	}
	if !strings.HasPrefix(o.Text, "Proved again just now. ") {
		t.Errorf("the text begins %q", o.Text)
	}

	o = s.tool("verify", map[string]any{"ids": []string{mod.ID}})
	if got := views(t, o.Data); o.Failed || len(got) != 1 || got[0].ID != mod.ID || got[0].Status != fact.Holds {
		t.Errorf("verify of one fact returned %+v (%s)", got, o.Text)
	}

	o = s.tool("verify", map[string]any{"ids": []string{mod.ID, "f-zzzzzz"}})
	if want := `there is no fact with the id "f-zzzzzz"`; !o.Failed || o.Text != want || o.Data != nil {
		t.Errorf("verify of an unknown id answered (failed %v) %q, want the error %q", o.Failed, o.Text, want)
	}
}

func TestRetractWithdrawsAFactForGood(t *testing.T) {
	s := open(t)
	s.handshake("claude-code")
	v := s.remember("The API listens on port 8080", "port localhost:8080")

	o := s.tool("retract", map[string]any{"id": v.ID, "reason": "the service moved into a container"})
	if o.Failed {
		t.Fatalf("retract was turned down: %s", o.Text)
	}
	gone := view(t, o.Data["fact"])
	sameAsText(t, o, "Withdrawn. It will not be recalled again.", gone)
	r := gone.Retracted
	if gone.ID != v.ID || r == nil || r.By != "claude-code" || r.Reason != "the service moved into a container" || !r.At.Equal(start) {
		t.Errorf("the withdrawn fact is %s with %+v, want it to keep who withdrew it, when and why", gone.ID, r)
	}
	if len(o.Data) != 1 {
		t.Errorf("the structured answer has more than the fact: %v", keys(o.Data))
	}
	if got := views(t, s.tool("recall", nil).Data); len(got) != 0 {
		t.Errorf("a withdrawn fact was recalled: %+v", got)
	}
	kept, err := s.store.Get(v.ID)
	if err != nil || kept.Retracted == nil {
		t.Errorf("the withdrawn fact is no longer in the history: %+v, %v", kept, err)
	}
}

func TestRetractTurnsDownWhatItCannotDo(t *testing.T) {
	s := open(t)
	v := s.remember("The API listens on port 8080", "port localhost:8080")
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"no reason", map[string]any{"id": v.ID}, "say why " + v.ID + " is being retracted, in a few words: the reason is kept with it"},
		{"a blank reason", map[string]any{"id": v.ID, "reason": " \t "}, "say why " + v.ID + " is being retracted, in a few words: the reason is kept with it"},
		{"an id nobody has", map[string]any{"id": "f-zzzzzz", "reason": "wrong"}, `there is no fact with the id "f-zzzzzz"`},
		{"no id", map[string]any{"reason": "wrong"}, `there is no fact with the id ""`},
		{"a reason that is not text", map[string]any{"id": v.ID, "reason": 5}, `the arguments are not right: "reason" is the wrong kind of value (number)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := s.tool("retract", tc.args)
			if !o.Failed || !strings.HasPrefix(o.Text, tc.want) || o.Data != nil {
				t.Errorf("answered (failed %v) %q, want the error %q", o.Failed, o.Text, tc.want)
			}
			if kept, err := s.store.Get(v.ID); err != nil || !kept.Live() {
				t.Errorf("the fact was withdrawn all the same: %+v, %v", kept, err)
			}
		})
	}
	t.Run("twice", func(t *testing.T) {
		args := map[string]any{"id": v.ID, "reason": "the service was removed"}
		if o := s.tool("retract", args); o.Failed {
			t.Fatalf("the first retract was turned down: %s", o.Text)
		}
		o := s.tool("retract", args)
		if want := v.ID + " was already retracted"; !o.Failed || o.Text != want {
			t.Errorf("the second retract answered (failed %v) %q, want the error %q", o.Failed, o.Text, want)
		}
	})
}

// A tool that does not exist, or a call that names none, is a mistake in the
// protocol, not something for the model to read: it is a JSON-RPC error.
func TestACallThatNamesNoKnownToolIsAProtocolError(t *testing.T) {
	s := open(t)
	for _, tc := range []struct {
		name   string
		params any
		want   string
	}{
		{"a tool that does not exist", map[string]any{"name": "forget", "arguments": map[string]any{}}, "unknown tool: forget"},
		{"a tool of another server", map[string]any{"name": "Recall"}, "unknown tool: Recall"},
		{"no name", map[string]any{"arguments": map[string]any{"query": "port"}}, "tools/call needs a tool name"},
		{"an empty name", map[string]any{"name": ""}, "tools/call needs a tool name"},
		{"a name that is not text", map[string]any{"name": 7}, "tools/call needs a tool name"},
		{"no parameters", nil, "tools/call needs a tool name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.fault("tools/call", tc.params, -32602); got.Message != tc.want {
				t.Errorf("the message is %q, want %q", got.Message, tc.want)
			}
		})
	}
	s.alive()
}

func TestAnUnknownMethodIsAnsweredWithMethodNotFound(t *testing.T) {
	s := open(t)
	s.handshake("claude-code")
	for _, method := range []string{
		"resources/list", "prompts/list", "completion/complete", "logging/setLevel",
		"tools/call/remember", "Ping", "remember", "notifications/initialized",
	} {
		if got := s.fault(method, map[string]any{}, -32601); got.Message != "method not found: "+method {
			t.Errorf("%s: the message is %q", method, got.Message)
		}
	}
	s.alive()
}

func TestParametersMustBeAnObject(t *testing.T) {
	s := open(t)
	for _, params := range []any{[]int{1, 2}, "port", 7, true} {
		for _, method := range []string{"tools/call", "tools/list", "ping", "initialize"} {
			if got := s.fault(method, params, -32602); got.Message != "params must be an object" {
				t.Errorf("%s with the parameters %v: the message is %q", method, params, got.Message)
			}
		}
	}
	s.alive()
}

// broken is a store that fails: a full disk, say.
type broken struct {
	store.Store
	err error
}

func (b broken) Facts() ([]*fact.Fact, error) { return nil, b.err }

func (b broken) Add(*fact.Fact, *fact.Evidence) (*fact.Fact, error) { return nil, b.err }

// When Holdfast itself fails, the agent is told in so many words, so that it
// does not take silence or an empty answer for "nothing is known". The detail
// also goes to the log, and never to the output.
func TestAFaultInsideHoldfastIsToldToTheAgentAndLogged(t *testing.T) {
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"recall", map[string]any{"query": "port"}},
		{"verify", nil},
		{"remember", map[string]any{"claim": "The API listens on port 8080", "check": "port localhost:8080"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			s := open(t, func(srv *Server) {
				srv.Engine.Store = broken{Store: srv.Engine.Store, err: errors.New("the disk is full")}
			})
			o := s.tool(tc.tool, tc.args)
			if want := "Holdfast could not do that: the disk is full"; !o.Failed || o.Text != want {
				t.Errorf("answered (failed %v) %q, want the error %q", o.Failed, o.Text, want)
			}
			if o.Data != nil {
				t.Errorf("a failed call carries structured data: %v", o.Data)
			}
			s.alive()
			s.silence()
			if want := "holdfast mcp: " + tc.tool + ": the disk is full\n"; s.log.String() != want {
				t.Errorf("the log is %q, want %q", s.log.String(), want)
			}
		})
	}
}

// With no log to write to, a fault is still answered and nothing breaks.
func TestAServerWithoutALogStillAnswersAFault(t *testing.T) {
	s := open(t, func(srv *Server) {
		srv.Log = nil
		srv.Engine.Store = broken{Store: srv.Engine.Store, err: errors.New("the disk is full")}
	})
	if o := s.tool("recall", nil); !o.Failed || o.Text != "Holdfast could not do that: the disk is full" {
		t.Errorf("answered (failed %v) %q", o.Failed, o.Text)
	}
}

// wild is a store with a bug in it.
type wild struct{ store.Store }

func (wild) Facts() ([]*fact.Fact, error) { panic("index out of range") }

// A bug that panics inside one call must not take the server down with it:
// the call is answered with an internal error and the next one is served.
func TestAPanicInsideACallIsAnsweredAndTheServerKeepsServing(t *testing.T) {
	s := open(t, func(srv *Server) { srv.Engine.Store = wild{srv.Engine.Store} })
	got := s.fault("tools/call", map[string]any{"name": "recall"}, -32603)
	if got.Message != "internal error" {
		t.Errorf("the message is %q, want no more than \"internal error\"", got.Message)
	}
	s.alive()
	s.silence()
	if log := s.log.String(); !strings.HasPrefix(log, "holdfast mcp: panic in tools/call: index out of range") {
		t.Errorf("the log does not say what happened: %q", log)
	}
}

// Whose name a fact is stored under: the name the server was started with
// when there is one, else the name the client gives in the request, else the
// name it gave when it opened the session.
func TestWhoAFactIsStoredBy(t *testing.T) {
	named := func(name string) map[string]any {
		return map[string]any{metaVersion: Modern, metaClient: map[string]any{"name": name, "version": "3.0"}}
	}
	for _, tc := range []struct {
		name      string
		agent     string         // the name the server was started with
		handshake string         // the name given in "initialize", if any
		meta      map[string]any // the metadata of the request, if any
		want      string
	}{
		{"the name from the handshake", "", "claude-code", nil, "claude-code"},
		{"the name in the request", "", "", named("cursor"), "cursor"},
		{"the request's name before the handshake's", "", "claude-code", named("cursor"), "cursor"},
		{"the server's own name before the handshake's", "reviewer", "claude-code", nil, "reviewer"},
		{"the server's own name before the request's", "reviewer", "", named("cursor"), "reviewer"},
		{"a request of the new kind that names nobody, after a handshake", "", "claude-code", map[string]any{metaVersion: Modern}, "claude-code"},
		{"nobody", "", "", nil, ""},
		{"a request of the new kind that names nobody", "", "", map[string]any{metaVersion: Modern}, ""},
		{"client information that is not an object", "", "", map[string]any{metaVersion: Modern, metaClient: "cursor"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t, func(srv *Server) { srv.Agent = tc.agent })
			if tc.handshake != "" {
				s.handshake(tc.handshake)
			}
			o := s.toolWith(tc.meta, "remember", map[string]any{"claim": "The API listens on port 8080", "check": "port localhost:8080"})
			if o.Failed {
				t.Fatalf("remember was turned down: %s", o.Text)
			}
			v := view(t, o.Data["fact"])
			if v.By != tc.want {
				t.Errorf("the fact is by %q, want %q", v.By, tc.want)
			}
			kept, err := s.store.Get(v.ID)
			if err != nil || kept.By != tc.want {
				t.Errorf("the store has it by %q (%v), want %q", kept.By, err, tc.want)
			}
			o = s.toolWith(tc.meta, "retract", map[string]any{"id": v.ID, "reason": "only a test"})
			if o.Failed {
				t.Fatalf("retract was turned down: %s", o.Text)
			}
			if r := view(t, o.Data["fact"]).Retracted; r == nil || r.By != tc.want {
				t.Errorf("it was withdrawn by %+v, want by %q", r, tc.want)
			}
		})
	}
}

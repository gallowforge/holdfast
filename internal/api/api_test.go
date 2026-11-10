package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/engine"
	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/sim"
	"github.com/gallowforge/holdfast/internal/store"
)

var start = time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC)

const (
	testPolicy = "files .\nnet loopback\n"
	goMod      = "module example.com/atlas\n\ngo 1.24\n"
)

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

// page is the API as a page would have it, on a pretend machine with a few
// files and one open port, and an empty store that lives in memory.
type page struct {
	t     testing.TB
	api   *API
	world *sim.World
	store *store.Memory
	clock *clock
}

func open(t testing.TB) *page {
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
	s := store.NewMemory(rules)
	return &page{t: t, world: w, store: s, clock: c, api: &API{
		Engine:  &engine.Engine{Store: s, World: w},
		Version: "1.2.3-test",
		Who:     "alex",
		Where:   func() Where { return Where{Project: "atlas", Policy: testPolicy, Approved: true} },
	}}
}

// answer is a reply the way the page receives it: as JSON, read back.
type answer struct {
	Status  int
	OK      bool
	Error   string
	Refused bool
	// Result is the result member, and Members every member of the envelope.
	Result  map[string]any
	Members map[string]json.RawMessage
	Raw     json.RawMessage
}

// do makes one call and checks what every answer promises, whatever it says:
// the envelope has only the members a page knows, and the status goes with it.
func (p *page) do(method, args string) answer {
	p.t.Helper()
	var raw json.RawMessage
	if args != "" {
		raw = json.RawMessage(args)
	}
	return p.read(method, args, p.api.Do)(context.Background(), method, raw)
}

func (p *page) read(method, args string, do func(context.Context, string, json.RawMessage) (Reply, int)) func(context.Context, string, json.RawMessage) answer {
	return func(ctx context.Context, m string, raw json.RawMessage) answer {
		p.t.Helper()
		reply, status := do(ctx, m, raw)
		return check(p.t, method, args, reply, status)
	}
}

func check(t testing.TB, method, args string, reply Reply, status int) answer {
	t.Helper()
	text, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("%s %s: the reply cannot be written as JSON: %v", method, args, err)
	}
	a := answer{Status: status, Raw: text}
	if err := json.Unmarshal(text, &a.Members); err != nil {
		t.Fatalf("%s %s: the reply is not a JSON object: %s", method, args, text)
	}
	for name := range a.Members {
		switch name {
		case "ok", "result", "error", "refused":
		default:
			t.Errorf("%s %s: the reply has a member %q the page does not know", method, args, name)
		}
	}
	if _, ok := a.Members["ok"]; !ok {
		t.Errorf("%s %s: the reply does not say whether it worked: %s", method, args, text)
	}
	var plain struct {
		OK      bool            `json:"ok"`
		Result  json.RawMessage `json:"result"`
		Error   string          `json:"error"`
		Refused bool            `json:"refused"`
	}
	if err := json.Unmarshal(text, &plain); err != nil {
		t.Fatalf("%s %s: the reply does not read back: %v", method, args, err)
	}
	a.OK, a.Error, a.Refused = plain.OK, plain.Error, plain.Refused
	if len(plain.Result) > 0 {
		if err := json.Unmarshal(plain.Result, &a.Result); err != nil {
			t.Fatalf("%s %s: the result is not an object: %s", method, args, plain.Result)
		}
	}

	// What the envelope and the status say must agree.
	switch status {
	case 200:
		if !a.OK || a.Error != "" || a.Refused || a.Result == nil {
			t.Errorf("%s %s: status 200 with %s", method, args, text)
		}
	case 404, 500:
		if a.OK || a.Error == "" || a.Refused || a.Result != nil {
			t.Errorf("%s %s: status %d with %s", method, args, status, text)
		}
	case 422:
		if a.OK || a.Error == "" || !a.Refused || a.Result != nil {
			t.Errorf("%s %s: status 422 with %s", method, args, text)
		}
	default:
		t.Errorf("%s %s: status %d, which Do never promised", method, args, status)
	}
	return a
}

// good makes a call that must work and returns its result.
func (p *page) good(method, args string) map[string]any {
	p.t.Helper()
	a := p.do(method, args)
	if a.Status != 200 {
		p.t.Fatalf("%s %s answered %d: %s", method, args, a.Status, a.Raw)
	}
	return a.Result
}

// refused makes a call that must be turned down as the caller's mistake, and
// returns the reason.
func (p *page) refused(method, args string) string {
	p.t.Helper()
	a := p.do(method, args)
	if a.Status != 422 {
		p.t.Fatalf("%s %s answered %d, want it refused with 422: %s", method, args, a.Status, a.Raw)
	}
	return a.Error
}

// view reads one fact out of a result.
func view(t testing.TB, from any) engine.View {
	t.Helper()
	text, err := json.Marshal(from)
	if err != nil {
		t.Fatal(err)
	}
	var v engine.View
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("this is not a fact as the page knows it: %v: %s", err, text)
	}
	if v.ID == "" {
		t.Fatalf("this fact has no id: %s", text)
	}
	return v
}

func views(t testing.TB, from any) []engine.View {
	t.Helper()
	list, ok := from.([]any)
	if !ok {
		t.Fatalf("facts = %v, want a list", from)
	}
	out := make([]engine.View, 0, len(list))
	for _, one := range list {
		out = append(out, view(t, one))
	}
	return out
}

func ids(list []engine.View) []string {
	out := []string{}
	for _, v := range list {
		out = append(out, v.ID)
	}
	return out
}

// quote writes a value as JSON, for the arguments of a call.
func quote(v any) string {
	text, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(text)
}

// remember stores one fact through the API and returns it.
func (p *page) remember(claim, check string) engine.View {
	p.t.Helper()
	got := p.good("remember", quote(map[string]any{"claim": claim, "check": check}))
	return view(p.t, got["fact"])
}

// claims is what the store holds, live or not.
func (p *page) claims() []string {
	p.t.Helper()
	all, err := p.store.Facts()
	if err != nil {
		p.t.Fatal(err)
	}
	out := []string{}
	for _, f := range all {
		out = append(out, f.Claim)
	}
	return out
}

func counts(t testing.TB, from any) engine.Counts {
	t.Helper()
	text, _ := json.Marshal(from)
	var c engine.Counts
	if err := json.Unmarshal(text, &c); err != nil {
		t.Fatalf("counts = %s: %v", text, err)
	}
	return c
}

// --- state ---

func TestStateOfAnEmptyStoreIsStillAWholePage(t *testing.T) {
	p := open(t)
	for _, args := range []string{"", "{}", "null"} {
		got := p.good("state", args)
		if got["version"] != "1.2.3-test" || got["demo"] != false {
			t.Errorf("version %v, demo %v, want 1.2.3-test and not the demo", got["version"], got["demo"])
		}
		if got["project"] != "atlas" || got["policy"] != testPolicy || got["approved"] != true {
			t.Errorf("project %v, policy %q, approved %v", got["project"], got["policy"], got["approved"])
		}
		if now, _ := got["now"].(string); now != start.Format(time.RFC3339Nano) {
			t.Errorf("now = %v, want the machine's clock, %s", got["now"], start.Format(time.RFC3339Nano))
		}
		// The page walks the list without asking whether there is one.
		if list, ok := got["facts"].([]any); !ok || len(list) != 0 {
			t.Errorf("facts = %#v, want an empty list and never null", got["facts"])
		}
		if gone, there := got["gone"]; there {
			if _, ok := gone.([]any); !ok {
				t.Errorf("gone = %#v, want a list or nothing", gone)
			}
		}
		if c := counts(t, got["counts"]); c != (engine.Counts{}) {
			t.Errorf("counts = %+v, want all zero", c)
		}
		if note, there := got["note"]; there {
			t.Errorf("note = %v, but the policy is in force", note)
		}
	}
}

func TestStateListsWhatIsKnownWithoutProvingAnything(t *testing.T) {
	p := open(t)
	port := p.remember("The API listens on port 8080", "port localhost:8080")
	mod := p.remember("The module needs Go 1.24", `contains go.mod "go 1.24"`)
	old := p.remember("Staging runs on port 9090", "not port localhost:9090")
	p.good("retract", quote(map[string]any{"id": old.ID, "reason": "staging is gone"}))

	// The port closes, but nobody has proved anything since.
	p.clock.add(time.Hour)
	p.world.Change(func(w *sim.World) { w.Ports["localhost:8080"] = false })

	got := p.good("state", "")
	list := views(t, got["facts"])
	if len(list) != 2 {
		t.Fatalf("state lists %v, want the two live facts", ids(list))
	}
	for _, v := range list {
		if v.Status != fact.Holds {
			t.Errorf("%s is shown as %s: state ran a check, or made a status up", v.ID, v.Status)
		}
		if v.By != "alex" {
			t.Errorf("%s is by %q, want the person at the page, alex", v.ID, v.By)
		}
	}
	if c := counts(t, got["counts"]); c.Total != 2 || c.Holds != 2 {
		t.Errorf("counts = %+v, want 2 facts that hold", c)
	}
	gone := views(t, got["gone"])
	if len(gone) != 1 || gone[0].ID != old.ID || gone[0].Retracted == nil || gone[0].Retracted.Reason != "staging is gone" {
		t.Errorf("gone = %+v, want the retracted fact with its reason", gone)
	}
	if now, _ := got["now"].(string); now != start.Add(time.Hour).Format(time.RFC3339Nano) {
		t.Errorf("now = %v, want the clock an hour on", got["now"])
	}

	// Proving is what finds out.
	p.good("verify", "")
	after := views(t, p.good("state", "")["facts"])
	if after[0].ID != port.ID || after[0].Status != fact.Broken || after[1].ID != mod.ID || after[1].Status != fact.Holds {
		t.Errorf("after a verify the state lists %s %s, %s %s; want the broken port first, then the module",
			after[0].ID, after[0].Status, after[1].ID, after[1].Status)
	}
}

func TestStateSaysWhetherThisIsTheDemoAndWhereItLooks(t *testing.T) {
	p := open(t)
	p.api.More = func(context.Context, string, json.RawMessage) (any, bool, error) { return nil, false, nil }
	if got := p.good("state", ""); got["demo"] != true {
		t.Errorf("demo = %v, want true once the demo's calls are there", got["demo"])
	}

	p.api.More = nil
	p.api.Where = func() Where { return Where{Project: "billing", Policy: "files .\n", Approved: false} }
	got := p.good("state", "")
	if got["project"] != "billing" || got["policy"] != "files .\n" || got["approved"] != false || got["demo"] != false {
		t.Errorf("project %v, policy %q, approved %v, demo %v", got["project"], got["policy"], got["approved"], got["demo"])
	}

	// A page with nowhere to describe still opens.
	p.api.Where = nil
	got = p.good("state", "")
	if got["project"] != "" || got["policy"] != "" || got["approved"] != false {
		t.Errorf("with no store to describe: project %v, policy %v, approved %v", got["project"], got["policy"], got["approved"])
	}
}

// --- remember ---

func TestRememberStoresAProvedFactInThePersonsName(t *testing.T) {
	p := open(t)
	got := p.good("remember", `{"claim":"The API listens on port 8080","check":"port localhost:8080","tags":["api","ports"]}`)
	v := view(t, got["fact"])
	if got["already"] != false {
		t.Errorf("already = %v for a new fact", got["already"])
	}
	if v.Status != fact.Holds || v.By != "alex" || v.Check != "port localhost:8080" || v.Kind != "port" {
		t.Errorf("stored %+v, want a port fact that holds, by alex", v)
	}
	if !reflect.DeepEqual(v.Tags, []string{"api", "ports"}) {
		t.Errorf("tags = %v", v.Tags)
	}
	kept, err := p.store.Get(v.ID)
	if err != nil || kept.Claim != "The API listens on port 8080" || kept.By != "alex" {
		t.Errorf("the store holds %+v, %v", kept, err)
	}

	// The same fact again is not stored twice.
	again := p.good("remember", `{"claim":"The API listens on port 8080","check":"port localhost:8080"}`)
	if again["already"] != true || view(t, again["fact"]).ID != v.ID {
		t.Errorf("the same fact again answered already=%v with %v, want the first one back", again["already"], again["fact"])
	}
	if got := p.claims(); len(got) != 1 {
		t.Errorf("the store holds %v, want one fact", got)
	}
}

func TestRememberRefusesAFactWhoseCheckFails(t *testing.T) {
	p := open(t)
	for _, tc := range []struct {
		name, args, want string
	}{
		{"a port that is closed", `{"claim":"The database listens on 5432","check":"port localhost:5432"}`,
			"the check fails right now, so this is not a fact yet: "},
		{"a file that is not there", `{"claim":"There is a Makefile","check":"file Makefile"}`,
			"the check fails right now, so this is not a fact yet: "},
		{"no claim", `{"check":"file go.mod"}`, "the claim is empty: say the fact in one plain sentence"},
		{"a check in no known language", `{"claim":"The API is up","check":"is it up"}`, `unknown check "is". A check starts with one of: `},
		{"a time to live that is not one", `{"claim":"The release is on Friday","ttl":"soon"}`, `duration "soon": the unit must be s, m, h, d or w`},
		{"an id to replace that is not one", `{"claim":"The API listens on 8080","replaces":"the old one"}`,
			`"the old one" is not a fact id; ids look like f-abc234`},
		{"an id to replace that nobody has", `{"claim":"The API listens on 8080","replaces":"f-zzzzzz"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			why := p.refused("remember", tc.args)
			if !strings.HasPrefix(why, tc.want) {
				t.Errorf("the reason is %q, want it to begin %q", why, tc.want)
			}
			if got := p.claims(); len(got) != 0 {
				t.Errorf("something was stored all the same: %v", got)
			}
		})
	}
}

func TestRememberKeepsANoteAndABlockedFact(t *testing.T) {
	p := open(t)
	note := view(t, p.good("remember", `{"claim":"The release is cut on Fridays","ttl":"12h"}`)["fact"])
	if note.Status != fact.Unproven || note.Check != "" || note.StaleAt == nil || !note.StaleAt.Equal(start.Add(12*time.Hour)) {
		t.Errorf("the note is %+v, want it unproven and stale in 12 hours", note)
	}
	blocked := view(t, p.good("remember", `{"claim":"Billing answers","check":"http 200 https://example.com/health"}`)["fact"])
	if blocked.Status != fact.Blocked || !strings.Contains(blocked.Detail, "holdfast allow net example.com") {
		t.Errorf("the fact the policy will not let run is %s: %q, want blocked with the way to allow it", blocked.Status, blocked.Detail)
	}
}

func TestRememberWithReplacesWithdrawsTheOlderFact(t *testing.T) {
	p := open(t)
	old := p.remember("The config says production", `contains config/app.yaml "mode: production"`)
	got := p.good("remember", quote(map[string]any{"claim": "The config sets port 8080", "check": `contains config/app.yaml "port: 8080"`, "replaces": old.ID}))
	v := view(t, got["fact"])
	if v.Replaces != old.ID {
		t.Errorf("the new fact replaces %q, want %s", v.Replaces, old.ID)
	}
	kept, err := p.store.Get(old.ID)
	if err != nil || kept.Live() || kept.ReplacedBy != v.ID {
		t.Errorf("the older fact is %+v, %v; want it withdrawn in favour of %s", kept, err, v.ID)
	}
	state := p.good("state", "")
	if live := views(t, state["facts"]); len(live) != 1 || live[0].ID != v.ID {
		t.Errorf("the page lists %v, want only the new fact", ids(live))
	}
	if gone := views(t, state["gone"]); len(gone) != 1 || gone[0].ID != old.ID || gone[0].ReplacedBy != v.ID {
		t.Errorf("gone = %+v, want the older fact", gone)
	}
}

// --- recall ---

func TestRecallOfAnEmptyStoreIsAnAnswer(t *testing.T) {
	p := open(t)
	got := p.good("recall", `{"query":"port"}`)
	if got["matched"] != float64(0) || got["known"] != float64(0) {
		t.Errorf("matched %v, known %v, want none", got["matched"], got["known"])
	}
	if got["text"] != "Nothing is remembered here yet." {
		t.Errorf("text = %q", got["text"])
	}
	// An empty list and never null: a client reads the length without asking first.
	if list, ok := got["facts"].([]any); !ok || len(list) != 0 {
		t.Errorf("facts = %#v, want an empty list", got["facts"])
	}
}

func TestRecallProvesWhatItReturnsAndShowsTheAgentsText(t *testing.T) {
	p := open(t)
	port := p.remember("The API listens on port 8080", "port localhost:8080")
	mod := p.remember("The module needs Go 1.24", `contains go.mod "go 1.24"`)
	p.remember("The config says production", `contains config/app.yaml "mode: production"`)

	p.clock.add(time.Hour)
	p.world.Change(func(w *sim.World) { w.Ports["localhost:8080"] = false })

	got := p.good("recall", `{"query":"port api"}`)
	list := views(t, got["facts"])
	if len(list) != 1 || list[0].ID != port.ID || list[0].Status != fact.Broken {
		t.Fatalf("recall returned %+v, want the port fact, found broken", list)
	}
	if got["matched"] != float64(1) || got["known"] != float64(3) {
		t.Errorf("matched %v of %v known, want 1 of 3", got["matched"], got["known"])
	}
	// The text is the very block an agent is given for the same question.
	want, err := p.api.Engine.Recall(context.Background(), "port api", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got["text"] != want.Text(p.clock.now()) {
		t.Errorf("text =\n%s\nwant what the agent reads:\n%s", got["text"], want.Text(p.clock.now()))
	}
	if text, _ := got["text"].(string); !strings.Contains(text, "BROKEN") || !strings.Contains(text, port.ID) {
		t.Errorf("the text does not show the broken fact:\n%s", text)
	}

	// With no question, everything comes back, as many as asked for.
	all := p.good("recall", "")
	if list := views(t, all["facts"]); len(list) != 3 || all["matched"] != float64(3) {
		t.Errorf("recall of everything returned %v (matched %v), want all 3", ids(list), all["matched"])
	}
	two := p.good("recall", `{"limit":2}`)
	if list := views(t, two["facts"]); len(list) != 2 || two["matched"] != float64(3) {
		t.Errorf("recall with limit 2 returned %v (matched %v), want 2 of 3", ids(list), two["matched"])
	}
	one := p.good("recall", `{"query":"module","limit":1}`)
	if list := views(t, one["facts"]); len(list) != 1 || list[0].ID != mod.ID {
		t.Errorf("recall of the module returned %v", ids(list))
	}
}

// --- verify ---

func TestVerifyProvesEverythingOrTheFactsNamed(t *testing.T) {
	p := open(t)
	port := p.remember("The API listens on port 8080", "port localhost:8080")
	mod := p.remember("The module needs Go 1.24", `contains go.mod "go 1.24"`)
	p.good("remember", `{"claim":"The release is cut on Fridays"}`)

	p.clock.add(time.Hour)
	p.world.Change(func(w *sim.World) {
		w.Ports["localhost:8080"] = false
		w.Write("go.mod", "module example.com/atlas\n\ngo 1.25\n", start.Add(time.Hour))
	})

	// Only the one named is proved again.
	got := p.good("verify", quote(map[string]any{"ids": []string{mod.ID}}))
	list := views(t, got["facts"])
	if len(list) != 1 || list[0].ID != mod.ID || list[0].Status != fact.Broken {
		t.Fatalf("verify of one fact returned %+v, want it alone, broken", list)
	}
	if c := counts(t, got["counts"]); c.Total != 1 || c.Broken != 1 {
		t.Errorf("counts = %+v, want the one fact, broken", c)
	}
	if kept, _ := p.store.Get(port.ID); kept.Last == nil || kept.Last.Status != fact.Holds {
		t.Errorf("the port fact was proved too, though it was not named: %+v", kept.Last)
	}

	for _, args := range []string{"", "{}", "null", `{"ids":[]}`, `{"ids":null}`} {
		got := p.good("verify", args)
		if c := counts(t, got["counts"]); c.Broken != 2 || c.Holds != 0 {
			t.Errorf("verify %s: counts = %+v, want both checked facts broken", args, c)
		}
		for _, v := range views(t, got["facts"]) {
			if v.Check != "" && v.Status != fact.Broken {
				t.Errorf("verify %s: %s is %s", args, v.ID, v.Status)
			}
		}
	}

	why := p.refused("verify", `{"ids":["f-zzzzzz"]}`)
	if why != `there is no fact with the id "f-zzzzzz"` {
		t.Errorf("verify of an id nobody has: %q", why)
	}
}

// --- retract ---

func TestRetractNeedsAReasonAndWithdrawsForGood(t *testing.T) {
	p := open(t)
	v := p.remember("The API listens on port 8080", "port localhost:8080")

	for _, tc := range []struct{ name, args, want string }{
		{"no reason", quote(map[string]any{"id": v.ID}), "say why " + v.ID + " is being retracted, in a few words: the reason is kept with it"},
		{"a blank reason", quote(map[string]any{"id": v.ID, "reason": "  "}), "say why " + v.ID + " is being retracted, in a few words: the reason is kept with it"},
		{"an id nobody has", `{"id":"f-zzzzzz","reason":"wrong"}`, `there is no fact with the id "f-zzzzzz"`},
		{"no id", `{"reason":"wrong"}`, `there is no fact with the id ""`},
		{"no arguments", "", `there is no fact with the id ""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if why := p.refused("retract", tc.args); why != tc.want {
				t.Errorf("the reason is %q, want %q", why, tc.want)
			}
			if kept, err := p.store.Get(v.ID); err != nil || !kept.Live() {
				t.Errorf("the fact was withdrawn all the same: %+v, %v", kept, err)
			}
		})
	}

	p.clock.add(time.Minute)
	got := view(t, p.good("retract", quote(map[string]any{"id": " " + v.ID + " ", "reason": "the service was removed"}))["fact"])
	if got.ID != v.ID || got.Retracted == nil || got.Retracted.By != "alex" || got.Retracted.Reason != "the service was removed" ||
		!got.Retracted.At.Equal(start.Add(time.Minute)) {
		t.Errorf("retracted = %+v, want it withdrawn by alex, a minute in, with the reason", got.Retracted)
	}
	if list := views(t, p.good("state", "")["facts"]); len(list) != 0 {
		t.Errorf("the page still lists %v", ids(list))
	}
	if why := p.refused("retract", quote(map[string]any{"id": v.ID, "reason": "again"})); why != v.ID+" was already retracted" {
		t.Errorf("a second retract: %q", why)
	}
}

// --- vouch ---

func TestVouchRestartsANotesTimeAndOnlyANotes(t *testing.T) {
	p := open(t)
	note := view(t, p.good("remember", `{"claim":"The release is cut on Fridays","ttl":"12h"}`)["fact"])
	checked := p.remember("The API listens on port 8080", "port localhost:8080")
	gone := view(t, p.good("remember", `{"claim":"The old office is in Hawally"}`)["fact"])
	p.good("retract", quote(map[string]any{"id": gone.ID, "reason": "they moved"}))

	// A day on, the note has gone stale.
	p.clock.add(24 * time.Hour)
	for _, v := range views(t, p.good("state", "")["facts"]) {
		if v.ID == note.ID && v.Status != fact.Stale {
			t.Fatalf("a 12 hour note is %s after a day, want stale", v.Status)
		}
	}

	got := view(t, p.good("vouch", quote(map[string]any{"id": note.ID}))["fact"])
	if got.Status != fact.Unproven || got.VouchedBy != "alex" {
		t.Errorf("after vouching the note is %s, vouched by %q; want unproven again, by alex", got.Status, got.VouchedBy)
	}
	if got.StaleAt == nil || !got.StaleAt.Equal(start.Add(36*time.Hour)) {
		t.Errorf("stale_at = %v, want 12 hours from the moment it was vouched for", got.StaleAt)
	}

	for _, tc := range []struct{ name, args, want string }{
		{"a fact with a check", quote(map[string]any{"id": checked.ID}), checked.ID + " has a check, so it proves itself; there is nothing to vouch for"},
		{"a fact that was withdrawn", quote(map[string]any{"id": gone.ID}), gone.ID + " has been retracted or replaced"},
		{"an id nobody has", `{"id":"f-zzzzzz"}`, `there is no fact with the id "f-zzzzzz"`},
		{"no id", `{}`, `there is no fact with the id ""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if why := p.refused("vouch", tc.args); why != tc.want {
				t.Errorf("the reason is %q, want %q", why, tc.want)
			}
		})
	}
}

// --- parse ---

func TestParseSaysWhetherACheckIsWellFormedWithoutRunningIt(t *testing.T) {
	p := open(t)
	for _, tc := range []struct {
		check, kind, short string
	}{
		{"port localhost:8080", "port", "port localhost:8080"},
		{"  file   go.mod ", "file", "file go.mod"},
		{`contains go.mod "go 1.24"`, "contains", `contains go.mod "go 1.24"`},
		{"not port localhost:9090", "port", "not port localhost:9090"},
		{"http 200 https://example.com/health", "http", "http 200 https://example.com/health"},
		{"cmd make test", "cmd", "cmd make test"},
		// A check for something that is not so parses all the same: nothing is run.
		{"file no/such/file", "file", "file no/such/file"},
	} {
		got := p.good("parse", quote(map[string]any{"check": tc.check}))
		if got["ok"] != true || got["kind"] != tc.kind || got["check"] != tc.short {
			t.Errorf("parse %q = %v, want ok, kind %s, check %q", tc.check, got, tc.kind, tc.short)
		}
		if _, there := got["error"]; there {
			t.Errorf("parse %q: a check that is fine comes with an error: %v", tc.check, got["error"])
		}
	}
	for _, check := range []string{"is it up", "port", "file", `contains go.mod "unfinished`, "http twohundred http://localhost"} {
		got := p.good("parse", quote(map[string]any{"check": check}))
		why, _ := got["error"].(string)
		if got["ok"] != false || why == "" {
			t.Errorf("parse %q = %v, want not ok, with the reason", check, got)
		}
		if _, there := got["kind"]; there {
			t.Errorf("parse %q: a check that is wrong has a kind: %v", check, got["kind"])
		}
	}
	if got := p.claims(); len(got) != 0 {
		t.Errorf("parse stored something: %v", got)
	}
}

// --- what is turned down ---

func TestACallNobodyAnswersIsNotFound(t *testing.T) {
	p := open(t)
	for _, method := range []string{"", "nope", "State", "state ", "/state", "machine", "flip", "reset", "remember/x", "__proto__"} {
		a := p.do(method, "{}")
		if a.Status != 404 || a.Error != "there is no such call" {
			t.Errorf("%q answered %d %q, want 404 and \"there is no such call\"", method, a.Status, a.Error)
		}
		if _, there := a.Members["refused"]; there {
			t.Errorf("%q: a call that does not exist is not a refusal: %s", method, a.Raw)
		}
	}
}

// withArgs is every call that takes arguments.
var withArgs = []string{"verify", "recall", "remember", "retract", "vouch", "parse"}

func TestArgumentsTheCallDoesNotKnowAreRefused(t *testing.T) {
	p := open(t)
	p.remember("The API listens on port 8080", "port localhost:8080")
	for _, method := range withArgs {
		for _, tc := range []struct{ args, want string }{
			{`{"nope":1}`, `the request is not right: unknown field "nope"`},
			{`{"Nope":true,"id":"f-abc234"}`, `the request is not right: unknown field "Nope"`},
			{`[]`, "the request is not right: expected an object, got array"},
			{`"text"`, "the request is not right: expected an object, got string"},
			{`7`, "the request is not right: expected an object, got number"},
			{`{`, "the request is not right: "},
			{`{"id":}`, "the request is not right: "},
			{`not json`, "the request is not right: "},
			{`{} {}`, "the request is not right: there is something after the JSON value"},
			{`{}x`, "the request is not right: there is something after the JSON value"},
			{`{}]`, "the request is not right: there is something after the JSON value"},
		} {
			why := p.refused(method, tc.args)
			if !strings.HasPrefix(why, tc.want) {
				t.Errorf("%s %s: the reason is %q, want it to begin %q", method, tc.args, why, tc.want)
			}
			for _, word := range []string{"unmarshal", "Go ", "struct", "json:"} {
				if strings.Contains(why, word) {
					t.Errorf("%s %s: the reason %q speaks of %q, which means nothing to a person", method, tc.args, why, word)
				}
			}
		}
	}
	if got := p.claims(); len(got) != 1 {
		t.Errorf("a refused call changed the store: %v", got)
	}
	if kept, _ := p.store.Facts(); !kept[0].Live() {
		t.Error("a refused call withdrew the fact")
	}
}

func TestAnArgumentOfTheWrongKindIsRefusedInPlainWords(t *testing.T) {
	p := open(t)
	for _, tc := range []struct{ method, args, want string }{
		{"recall", `{"limit":"many"}`, `the request is not right: "limit" is the wrong kind of value (string)`},
		{"recall", `{"limit":1.5}`, `the request is not right: "limit" is the wrong kind of value (number 1.5)`},
		{"recall", `{"query":["api"]}`, `the request is not right: "query" is the wrong kind of value (array)`},
		{"verify", `{"ids":"all"}`, `the request is not right: "ids" is the wrong kind of value (string)`},
		{"remember", `{"claim":8080}`, `the request is not right: "claim" is the wrong kind of value (number)`},
		{"remember", `{"claim":"x","tags":"api"}`, `the request is not right: "tags" is the wrong kind of value (string)`},
		{"retract", `{"id":"f-abc234","reason":{}}`, `the request is not right: "reason" is the wrong kind of value (object)`},
		{"vouch", `{"id":true}`, `the request is not right: "id" is the wrong kind of value (bool)`},
		{"parse", `{"check":[]}`, `the request is not right: "check" is the wrong kind of value (array)`},
	} {
		if why := p.refused(tc.method, tc.args); why != tc.want {
			t.Errorf("%s %s: the reason is %q, want %q", tc.method, tc.args, why, tc.want)
		}
	}
	if got := p.claims(); len(got) != 0 {
		t.Errorf("a refused call stored something: %v", got)
	}
}

func TestNoArgumentsAtAllMeansTheDefaults(t *testing.T) {
	p := open(t)
	p.remember("The API listens on port 8080", "port localhost:8080")
	for _, method := range []string{"state", "verify", "recall"} {
		for _, raw := range []json.RawMessage{nil, {}, json.RawMessage("null"), json.RawMessage("{}"), json.RawMessage(" \n{ }\n ")} {
			reply, status := p.api.Do(context.Background(), method, raw)
			a := check(t, method, string(raw), reply, status)
			if a.Status != 200 {
				t.Errorf("%s with arguments %q answered %d: %s", method, raw, a.Status, a.Raw)
				continue
			}
			if list := views(t, a.Result["facts"]); len(list) != 1 {
				t.Errorf("%s with arguments %q returned %d facts, want the one", method, raw, len(list))
			}
		}
	}
}

// --- faults ---

// broken is a store whose disk has failed.
type broken struct {
	store.Store
	err error
}

func (b broken) Facts() ([]*fact.Fact, error)   { return nil, b.err }
func (b broken) Get(string) (*fact.Fact, error) { return nil, b.err }
func (b broken) Add(*fact.Fact, *fact.Evidence) (*fact.Fact, error) {
	return nil, b.err
}

// A fault inside Holdfast is told apart from a mistake of the caller's: the
// page shows the one as "something is wrong here" and the other as "fix what
// you typed".
func TestAFaultInsideHoldfastIsNotARefusal(t *testing.T) {
	for _, tc := range []struct{ method, args string }{
		{"state", ""},
		{"verify", ""},
		{"recall", `{"query":"port"}`},
		{"remember", `{"claim":"The API listens on port 8080","check":"port localhost:8080"}`},
		{"remember", `{"claim":"The release is cut on Fridays"}`},
		{"retract", `{"id":"f-abc234","reason":"wrong"}`},
		{"vouch", `{"id":"f-abc234"}`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			p := open(t)
			p.api.Engine.Store = broken{Store: p.store, err: errors.New("the disk is full")}
			a := p.do(tc.method, tc.args)
			if a.Status != 500 || a.Error != "Holdfast could not do that: the disk is full" {
				t.Errorf("answered %d %q, want 500 and the fault", a.Status, a.Error)
			}
			if _, there := a.Members["refused"]; there {
				t.Errorf("a fault is marked as the caller's to fix: %s", a.Raw)
			}
		})
	}

	// A call that is wrong in itself is refused before the store is asked.
	p := open(t)
	p.api.Engine.Store = broken{Store: p.store, err: errors.New("the disk is full")}
	if a := p.do("recall", `{"nope":1}`); a.Status != 422 {
		t.Errorf("a malformed call on a broken store answered %d, want 422", a.Status)
	}
	if a := p.do("parse", `{"check":"file go.mod"}`); a.Status != 200 {
		t.Errorf("parse needs no store, but answered %d on a broken one", a.Status)
	}
	if a := p.do("nope", ""); a.Status != 404 {
		t.Errorf("an unknown call on a broken store answered %d, want 404", a.Status)
	}
}

// A call whose caller has gone stores nothing and is not answered as done.
func TestACallWhoseCallerHasGoneStoresNothing(t *testing.T) {
	p := open(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reply, status := p.api.Do(ctx, "remember", json.RawMessage(`{"claim":"The API listens on port 8080","check":"port localhost:8080"}`))
	a := check(t, "remember", "after the caller left", reply, status)
	if a.Status == 200 {
		t.Errorf("a call nobody waits for was answered as done: %s", a.Raw)
	}
	if got := p.claims(); len(got) != 0 {
		t.Errorf("it stored %v", got)
	}
}

// --- the demo's own calls ---

func TestMoreAnswersOnlyWhatTheBuiltInCallsDoNot(t *testing.T) {
	p := open(t)
	var asked []string
	p.api.More = func(_ context.Context, method string, args json.RawMessage) (any, bool, error) {
		asked = append(asked, method+" "+string(args))
		switch method {
		case "machine":
			return map[string]any{"switches": []string{"api"}}, true, nil
		case "flip":
			return nil, true, &engine.Refused{Why: "there is no such switch"}
		case "burn":
			return nil, true, errors.New("the demo broke")
		}
		return nil, false, nil
	}

	if got := p.good("machine", `{"x":1}`); !reflect.DeepEqual(got["switches"], []any{"api"}) {
		t.Errorf("machine = %v, want what the demo answered", got)
	}
	if why := p.refused("flip", `{"id":"nope"}`); why != "there is no such switch" {
		t.Errorf("flip was refused with %q", why)
	}
	if a := p.do("burn", ""); a.Status != 500 || a.Error != "Holdfast could not do that: the demo broke" {
		t.Errorf("a fault in the demo answered %d %q", a.Status, a.Error)
	}
	if a := p.do("nope", ""); a.Status != 404 {
		t.Errorf("a call the demo does not know either answered %d, want 404", a.Status)
	}
	want := []string{`machine {"x":1}`, `flip {"id":"nope"}`, "burn ", "nope "}
	if !reflect.DeepEqual(asked, want) {
		t.Errorf("the demo was asked %q, want %q", asked, want)
	}

	// The built-in calls are never handed over, not even when they fail.
	asked = nil
	p.good("state", "")
	p.good("recall", "")
	p.good("parse", `{"check":"file go.mod"}`)
	p.refused("retract", `{"id":"f-zzzzzz","reason":"wrong"}`)
	p.refused("verify", `{"nope":1}`)
	if len(asked) != 0 {
		t.Errorf("the demo was asked for built-in calls: %q", asked)
	}
}

// --- the envelope ---

func TestTheEnvelopeSaysOnlyWhatIsSo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply Reply
		want  string
	}{
		{"a result", Reply{OK: true, Result: map[string]any{"n": 1}}, `{"ok":true,"result":{"n":1}}`},
		{"a refusal", Reply{Error: "say why", Refused: true}, `{"ok":false,"error":"say why","refused":true}`},
		{"a fault", Reply{Error: "Holdfast could not do that: the disk is full"}, `{"ok":false,"error":"Holdfast could not do that: the disk is full"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.reply)
			if err != nil || string(got) != tc.want {
				t.Errorf("the reply is written %s (%v), want %s", got, err, tc.want)
			}
		})
	}
}

func TestCallHandsBackTheErrorItselfSoTheCallerCanTellWhoseItIs(t *testing.T) {
	p := open(t)
	ctx := context.Background()
	var refused *engine.Refused
	if _, err := p.api.Call(ctx, "retract", json.RawMessage(`{"id":"f-zzzzzz","reason":"x"}`)); !errors.As(err, &refused) {
		t.Errorf("a refusal comes back as %T (%v), want *engine.Refused", err, err)
	}
	if _, err := p.api.Call(ctx, "recall", json.RawMessage(`{"nope":1}`)); !errors.As(err, &refused) {
		t.Errorf("bad arguments come back as %T (%v), want *engine.Refused", err, err)
	}
	if v, err := p.api.Call(ctx, "nope", nil); err == nil || errors.As(err, &refused) || v != nil {
		t.Errorf("an unknown call comes back as %v, %T (%v); want a plain error", v, err, err)
	}
	fault := errors.New("the disk is full")
	p.api.Engine.Store = broken{Store: p.store, err: fault}
	if _, err := p.api.Call(ctx, "state", nil); !errors.Is(err, fault) || errors.As(err, &refused) {
		t.Errorf("a fault comes back as %T (%v), want the fault itself", err, err)
	}
}

// --- anything at all ---

// FuzzDo throws any method and any bytes at the door. Whatever comes, it
// answers with one of its four statuses, an envelope that agrees with it,
// and never a fault: nothing a caller sends is Holdfast's own failure.
func FuzzDo(f *testing.F) {
	for _, method := range append([]string{"state", "", "nope", "machine"}, withArgs...) {
		for _, args := range []string{
			"", "null", "{}", "[]", "7", `"x"`, "true", "{", "}", `{"nope":1}`, "{} {}", "{}x", "\xef\xbb\xbf{}", "\x00", "{\"a\":\"\xff\"}",
			`{"claim":"The API listens on port 8080","check":"port localhost:8080"}`,
			`{"claim":"The module needs Go 1.24","check":"contains go.mod \"go 1.24\"","tags":["go","build"],"ttl":"2w"}`,
			`{"claim":"A note","ttl":"12h"}`,
			`{"claim":"A note","ttl":"soon"}`,
			`{"claim":"Billing answers","check":"http 200 https://example.com/health"}`,
			`{"claim":"Tests pass","check":"cmd make test"}`,
			`{"claim":"There is a Makefile","check":"file Makefile"}`,
			`{"claim":"x","replaces":"f-abc234"}`,
			`{"claim":"x","tags":["a","b","c","d","e","f","g","h","i"]}`,
			`{"claim":"","check":""}`,
			`{"claim":8080}`,
			`{"query":"port api","limit":3}`,
			`{"query":"","limit":-1}`,
			`{"limit":99999999999999999999}`,
			`{"limit":1.5}`,
			`{"ids":["f-abc234"]}`,
			`{"ids":[""," ","f-abc234","f-abc234"]}`,
			`{"ids":"all"}`,
			`{"id":"f-abc234","reason":"wrong"}`,
			`{"id":"f-abc234"}`,
			`{"id":"../../etc/passwd","reason":"x"}`,
			`{"check":"port localhost:8080"}`,
			`{"check":"not file \"a b\""}`,
			`{"check":"matches go.mod \"(\""}`,
			`{"check":"contains go.mod \"unfinished"}`,
		} {
			f.Add(method, []byte(args))
		}
	}
	known := map[string]bool{"state": true}
	for _, m := range withArgs {
		known[m] = true
	}
	f.Fuzz(func(t *testing.T, method string, raw []byte) {
		p := open(t)
		p.remember("The API listens on port 8080", "port localhost:8080")
		before := len(p.claims())

		reply, status := p.api.Do(context.Background(), method, json.RawMessage(raw))
		a := check(t, method, string(raw), reply, status)

		if (a.Status == 404) != !known[method] {
			t.Errorf("%q answered %d", method, a.Status)
		}
		if a.Status == 500 {
			t.Errorf("%s %q was answered as a fault inside Holdfast: %s", method, raw, a.Error)
		}
		// Only one whole JSON value, or nothing, is ever acted on.
		empty := len(bytes.TrimSpace(raw)) == 0
		if a.Status == 200 && method != "state" && !empty && !json.Valid(raw) {
			t.Errorf("%s acted on arguments that are not JSON: %q", method, raw)
		}
		if a.Status != 200 || (method != "remember" && method != "retract") {
			if after := len(p.claims()); after != before {
				t.Errorf("%s %q (status %d) changed how many facts are stored: %d, then %d", method, raw, a.Status, before, after)
			}
		}
		for _, word := range []string{"unmarshal", "Go struct", "Go value"} {
			if strings.Contains(a.Error, word) {
				t.Errorf("%s %q: the error %q speaks of %q", method, raw, a.Error, word)
			}
		}
	})
}

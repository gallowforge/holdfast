// These tests tell the pretend machine from the real one, and the real one
// does not exist in a browser build.
//
//go:build !js

package demo

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/api"
	"github.com/gallowforge/holdfast/internal/engine"
	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/sim"
	"github.com/gallowforge/holdfast/internal/world"
)

var start = time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC)

// every id the demo is seeded with.
var seeded = []string{fPort, fHealth, fGo, fRelease, fStaging, fDB, fFlags, fBackup, fDocker, fLegacy, fBilling, fBranch, fRefresh}

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

// visit is one visitor at the demo's page.
type visit struct {
	t     testing.TB
	demo  *Demo
	api   *api.API
	clock *clock
}

func arrive(t testing.TB) *visit {
	t.Helper()
	c := &clock{t: start}
	d := New(c.now)
	return &visit{t: t, demo: d, api: d.API("1.2.3-test"), clock: c}
}

// call makes one call through the door the page uses and reads the answer
// back as the page would: from JSON.
func (v *visit) call(method, args string, into any) (api.Reply, int) {
	v.t.Helper()
	var raw json.RawMessage
	if args != "" {
		raw = json.RawMessage(args)
	}
	reply, status := v.api.Do(context.Background(), method, raw)
	if status == 200 && into != nil {
		text, err := json.Marshal(reply.Result)
		if err != nil {
			v.t.Fatalf("%s: the result cannot be written as JSON: %v", method, err)
		}
		if err := json.Unmarshal(text, into); err != nil {
			v.t.Fatalf("%s: the result does not read back: %v: %s", method, err, text)
		}
	}
	return reply, status
}

func (v *visit) good(method, args string, into any) {
	v.t.Helper()
	if reply, status := v.call(method, args, into); status != 200 {
		v.t.Fatalf("%s %s answered %d: %s", method, args, status, reply.Error)
	}
}

// machine asks for the side panel.
func (v *visit) machine() Machine {
	v.t.Helper()
	var m Machine
	v.good("machine", "", &m)
	return m
}

// flip turns one switch over and returns the panel as it then stands.
func (v *visit) flip(id string) Machine {
	v.t.Helper()
	var m Machine
	v.good("flip", `{"id":"`+id+`"}`, &m)
	return m
}

// known is every live fact as the page lists it, by id, with nothing proved.
func (v *visit) known() map[string]engine.View {
	v.t.Helper()
	var s struct {
		Facts []engine.View `json:"facts"`
	}
	v.good("state", "", &s)
	out := map[string]engine.View{}
	for _, f := range s.Facts {
		out[f.ID] = f
	}
	return out
}

// proved proves everything again and returns the status of every fact.
func (v *visit) proved() map[string]fact.Status {
	v.t.Helper()
	v.good("verify", "", nil)
	out := map[string]fact.Status{}
	for id, f := range v.known() {
		out[id] = f.Status
	}
	return out
}

func on(m Machine) []string {
	out := []string{}
	for _, s := range m.Switches {
		if s.On {
			out = append(out, s.ID)
		}
	}
	return out
}

func find(t testing.TB, m Machine, id string) Switch {
	t.Helper()
	for _, s := range m.Switches {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("the machine has no switch %q", id)
	return Switch{}
}

// changed lists the facts whose status differs between two rounds.
func changed(before, after map[string]fact.Status) []string {
	out := []string{}
	for id, was := range before {
		if after[id] != was {
			out = append(out, id)
		}
	}
	for id := range after {
		if _, ok := before[id]; !ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func sorted(list []string) []string {
	out := append([]string{}, list...)
	sort.Strings(out)
	return out
}

// --- the project as a visitor finds it ---

func TestNewSeedsThirteenFactsWithAPast(t *testing.T) {
	v := arrive(t)
	listed, err := v.demo.Engine.List(true)
	if err != nil {
		t.Fatal(err)
	}
	if listed.Counts.Total != 13 || len(listed.Facts) != 13 {
		t.Fatalf("the demo opens with %d facts (counted %d), want 13", len(listed.Facts), listed.Counts.Total)
	}
	if len(listed.Gone) != 0 {
		t.Errorf("the demo opens with %d withdrawn facts, want none", len(listed.Gone))
	}
	// How many hold and how many do not is the demo's to decide; that every
	// fact is counted once is not.
	c := listed.Counts
	if sum := c.Holds + c.Broken + c.Blocked + c.Unproven + c.Stale; sum != c.Total {
		t.Errorf("the counts %+v do not add up to the total", c)
	}
	var got []string
	for _, f := range listed.Facts {
		got = append(got, f.ID)
	}
	if !reflect.DeepEqual(sorted(got), sorted(seeded)) {
		t.Errorf("the facts are %v, want %v", sorted(got), sorted(seeded))
	}

	authors := map[string]bool{}
	for _, f := range listed.Facts {
		if !fact.ValidID(f.ID) {
			t.Errorf("%s is not an id as Holdfast makes them", f.ID)
		}
		if f.Claim == "" || len(f.Tags) == 0 || f.By == "" {
			t.Errorf("%s has claim %q, tags %v, author %q; want all three", f.ID, f.Claim, f.Tags, f.By)
		}
		authors[f.By] = true
		if !f.Created.Before(start) {
			t.Errorf("%s was stored at %s, want a past: before the visitor arrived", f.ID, f.Created)
		}
		if f.Check == "" {
			// A note has nothing to prove it, so it never holds.
			if f.Status != fact.Unproven && f.Status != fact.Stale {
				t.Errorf("%s has no check and is %s", f.ID, f.Status)
			}
			if len(f.History) != 0 || f.CheckedAt != nil {
				t.Errorf("%s has no check, yet it has evidence", f.ID)
			}
			continue
		}
		// A fact with a check was proved when it was stored and again a few
		// minutes before the visitor arrived. Its history has one entry for
		// every time the answer changed, the first proof included.
		if len(f.History) == 0 {
			t.Errorf("%s has a check and no proof behind it", f.ID)
			continue
		}
		if first := f.History[0].At; !first.Equal(f.Created) {
			t.Errorf("%s was first proved at %s, want when it was stored, %s", f.ID, first, f.Created)
		}
		if f.CheckedAt == nil || !f.CheckedAt.Equal(start.Add(-7*time.Minute)) {
			t.Errorf("%s was last proved at %v, want seven minutes before the visit", f.ID, f.CheckedAt)
		}
		if last := f.History[len(f.History)-1]; f.Since == nil || !f.Since.Equal(last.At) || last.Status != f.Status {
			t.Errorf("%s is %s since %v, but its history ends with %s at %s", f.ID, f.Status, f.Since, last.Status, last.At)
		}
		for i := 1; i < len(f.History); i++ {
			if f.History[i].At.Before(f.History[i-1].At) {
				t.Errorf("%s: its history goes back in time at entry %d", f.ID, i)
			}
		}
		for _, ev := range f.History {
			if ev.Detail == "" {
				t.Errorf("%s has a proof that says nothing", f.ID)
			}
		}
	}
	// Three agents and one person.
	if len(authors) != 4 {
		t.Errorf("the facts were stored by %v, want four authors", authors)
	}
}

func TestStagingHadABadAfternoonFiveDaysAgo(t *testing.T) {
	v := arrive(t)
	f, err := v.demo.Engine.Get(fStaging)
	if err != nil {
		t.Fatal(err)
	}
	var story []fact.Status
	for _, ev := range f.History {
		story = append(story, ev.Status)
	}
	want := []fact.Status{fact.Holds, fact.Broken, fact.Holds}
	if !reflect.DeepEqual(story, want) {
		t.Fatalf("staging's history reads %v, want %v", story, want)
	}
	if down, up := f.History[1].At, f.History[2].At; !down.Equal(start.Add(-5*day-3*time.Hour)) || !up.Equal(start.Add(-5*day-50*time.Minute)) {
		t.Errorf("staging was down from %s to %s, want an afternoon five days ago", down, up)
	}
	if f.Status != fact.Holds || f.Since == nil || !f.Since.Equal(start.Add(-5*day-50*time.Minute)) {
		t.Errorf("staging is %s since %v, want it up since that afternoon", f.Status, f.Since)
	}
	// The story left no trace on the machine.
	if s := find(t, v.machine(), "staging"); s.On {
		t.Error("the staging switch is on as the visitor arrives")
	}
}

// With nothing flipped, the things the machine switches would break are in
// order: each switch offers to break something that works.
func TestWithEverySwitchOffWhatTheMachineSwitchesListHolds(t *testing.T) {
	v := arrive(t)
	if got := on(v.machine()); len(got) != 0 {
		t.Fatalf("the visitor arrives with switches on: %v", got)
	}
	now := v.proved()
	for _, s := range v.machine().Switches {
		for _, id := range s.Facts {
			switch s.Group {
			case "machine":
				if now[id] != fact.Holds {
					t.Errorf("%s is %s before anyone touched the %s switch, want holds", id, now[id], s.ID)
				}
			case "policy":
				if now[id] != fact.Blocked {
					t.Errorf("%s is %s before a person allowed it (switch %s), want blocked", id, now[id], s.ID)
				}
			}
		}
	}
	// Proving again changes nothing while the machine stands still.
	if diff := changed(now, v.proved()); len(diff) != 0 {
		t.Errorf("a second proof changed %v, though nothing happened", diff)
	}
}

func TestArrivingProvesNothingNew(t *testing.T) {
	v := arrive(t)
	before := v.known()
	// What the page shows on arrival is what a proof right now would find.
	now := v.proved()
	for id, f := range before {
		if now[id] != f.Status {
			t.Errorf("%s is shown as %s on arrival, but a proof finds it %s", id, f.Status, now[id])
		}
	}
}

// --- the side panel ---

func TestMachineShowsTheSwitchesAndTheNotesFile(t *testing.T) {
	v := arrive(t)
	m := v.machine()

	want := []string{"api", "go", "release", "staging", "db", "flags", "backup", "docker", "legacy", "billing"}
	var got []string
	known := map[string]bool{}
	for _, id := range seeded {
		known[id] = true
	}
	policies := 0
	for _, s := range m.Switches {
		got = append(got, s.ID)
		if s.Do == "" || s.Undo == "" || s.What == "" || s.Do == s.Undo {
			t.Errorf("switch %s is labelled %q / %q, saying %q; want all three", s.ID, s.Do, s.Undo, s.What)
		}
		if s.On {
			t.Errorf("switch %s is on as the visitor arrives", s.ID)
		}
		switch s.Group {
		case "machine":
		case "policy":
			policies++
		default:
			t.Errorf("switch %s is in the group %q, which the page does not know", s.ID, s.Group)
		}
		if len(s.Facts) == 0 {
			t.Errorf("switch %s bears on no fact", s.ID)
		}
		for _, id := range s.Facts {
			if !known[id] {
				t.Errorf("switch %s bears on %s, which is not a fact of the demo", s.ID, id)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the switches are %v, want %v", got, want)
	}
	if policies != 1 {
		t.Errorf("%d switches belong to a person's decision, want the one", policies)
	}

	if len(m.Notes) == 0 {
		t.Fatal("the notes file is empty")
	}
	for _, n := range m.Notes {
		if n.Question == "" || n.Query == "" || n.Line == "" {
			t.Errorf("a note has question %q, query %q, line %q; want all three", n.Question, n.Query, n.Line)
		}
		if !known[n.Fact] {
			t.Errorf("the note %q is about %s, which is not a fact of the demo", n.Line, n.Fact)
		}
	}
	if !m.Written.Equal(start.Add(-12 * day)) {
		t.Errorf("the notes file was written at %s, want twelve days before the visit", m.Written)
	}
}

func TestMachineIsWrittenForThePageWithTheNamesItKnows(t *testing.T) {
	v := arrive(t)
	reply, status := v.call("machine", "", nil)
	if status != 200 {
		t.Fatalf("machine answered %d: %s", status, reply.Error)
	}
	text, err := json.Marshal(reply.Result)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Switches []map[string]any `json:"switches"`
		Notes    []map[string]any `json:"notes"`
		Written  string           `json:"written"`
	}
	if err := json.Unmarshal(text, &m); err != nil {
		t.Fatal(err)
	}
	keys := func(of map[string]any) []string {
		out := []string{}
		for k := range of {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if got, want := keys(m.Switches[0]), []string{"do", "facts", "group", "id", "on", "undo", "what"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a switch is written with %v, want %v", got, want)
	}
	if got, want := keys(m.Notes[0]), []string{"fact", "line", "query", "question"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a note is written with %v, want %v", got, want)
	}
	if m.Written == "" {
		t.Error("the panel does not say when the notes file was written")
	}
}

// The page sets one line of the notes file beside what Holdfast answers to
// the same question, asked for one fact. That answer must be the fact the
// line is about, or the comparison is between two different things.
func TestEveryPreparedQuestionIsAnsweredByTheFactItsNoteIsAbout(t *testing.T) {
	ask := func(t *testing.T, v *visit) {
		for _, n := range v.machine().Notes {
			var got struct {
				Facts []engine.View `json:"facts"`
				Text  string        `json:"text"`
			}
			v.good("recall", `{"query":"`+n.Query+`","limit":1}`, &got)
			if len(got.Facts) != 1 || got.Facts[0].ID != n.Fact {
				t.Errorf("%q (recall %q) is answered with %+v, want the fact the note is about, %s", n.Question, n.Query, got.Facts, n.Fact)
				continue
			}
			if !strings.Contains(got.Text, n.Fact) {
				t.Errorf("the text for the agent does not show %s:\n%s", n.Fact, got.Text)
			}
		}
	}
	t.Run("as the visitor arrives", func(t *testing.T) { ask(t, arrive(t)) })
	// Which fact answers a question must not depend on how the facts stand.
	t.Run("with every switch flipped", func(t *testing.T) {
		v := arrive(t)
		for _, s := range v.machine().Switches {
			v.flip(s.ID)
		}
		v.proved()
		ask(t, v)
	})
}

// --- flipping ---

func TestFlippingASwitchChangesExactlyTheFactsItListsAndFlippingBackRestoresThem(t *testing.T) {
	for _, s := range arrive(t).machine().Switches {
		t.Run(s.ID, func(t *testing.T) {
			v := arrive(t)
			before := v.proved()

			m := v.flip(s.ID)
			if got := on(m); !reflect.DeepEqual(got, []string{s.ID}) {
				t.Fatalf("after flipping %s the switches that are on are %v", s.ID, got)
			}
			// Nothing has been proved since, so the page still says what it said:
			// that is the point of the demo.
			for id, f := range v.known() {
				if f.Status != before[id] {
					t.Errorf("%s turned %s without being proved again", id, f.Status)
				}
			}

			after := v.proved()
			if got, want := changed(before, after), sorted(s.Facts); !reflect.DeepEqual(got, want) {
				t.Errorf("flipping %s changed %v, want exactly the facts it lists, %v", s.ID, got, want)
			}
			for _, id := range s.Facts {
				switch s.Group {
				case "machine":
					if after[id] != fact.Broken {
						t.Errorf("%s is %s after %q, want broken", id, after[id], s.Do)
					}
				case "policy":
					if after[id] != fact.Holds {
						t.Errorf("%s is %s after %q, want holds: the check may run now, and passes", id, after[id], s.Do)
					}
				}
			}

			m = v.flip(s.ID)
			if got := on(m); len(got) != 0 {
				t.Fatalf("after flipping %s back the switches that are on are %v", s.ID, got)
			}
			if diff := changed(before, v.proved()); len(diff) != 0 {
				t.Errorf("after flipping %s back, %v did not go back to what they were", s.ID, diff)
			}
		})
	}
}

func TestABrokenFactSaysWhatTheMachineSaysNow(t *testing.T) {
	v := arrive(t)
	v.flip("go")
	v.flip("api")
	v.proved()
	now := v.known()
	if f := now[fGo]; f.Status != fact.Broken || f.Detail == "" || f.Held == nil || !f.Held.Equal(start.Add(-7*time.Minute)) {
		t.Errorf("the Go fact is %s (%q), last held %v; want broken, with the reason, last held seven minutes before the visit", f.Status, f.Detail, f.Held)
	}
	for _, id := range []string{fPort, fHealth} {
		if f := now[id]; f.Status != fact.Broken || f.Detail == "" {
			t.Errorf("%s is %s (%q) with the API stopped", id, f.Status, f.Detail)
		}
	}
}

func TestSwitchesAddUpAndResetPutsEverythingBack(t *testing.T) {
	v := arrive(t)
	before := v.proved()

	var want []string
	for _, id := range []string{"api", "flags", "billing", "legacy"} {
		v.flip(id)
		want = append(want, find(t, v.machine(), id).Facts...)
	}
	if got := on(v.machine()); !reflect.DeepEqual(got, []string{"api", "flags", "legacy", "billing"}) {
		t.Fatalf("the switches that are on are %v", got)
	}
	if got := changed(before, v.proved()); !reflect.DeepEqual(got, sorted(want)) {
		t.Errorf("four switches changed %v, want %v", got, sorted(want))
	}

	var m Machine
	v.good("reset", "", &m)
	if got := on(m); len(got) != 0 {
		t.Errorf("after a reset these are still on: %v", got)
	}
	if diff := changed(before, v.proved()); len(diff) != 0 {
		t.Errorf("after a reset %v did not go back to what they were", diff)
	}
	// A reset with nothing to reset is fine too.
	v.good("reset", "", &m)
	if got := on(m); len(got) != 0 {
		t.Errorf("a second reset turned on %v", got)
	}
}

func TestTheBillingSwitchIsADecisionAboutThePolicy(t *testing.T) {
	v := arrive(t)
	var s struct {
		Policy   string `json:"policy"`
		Approved bool   `json:"approved"`
		Project  string `json:"project"`
		Demo     bool   `json:"demo"`
		Version  string `json:"version"`
	}
	v.good("state", "", &s)
	if s.Project != "atlas" || !s.Demo || !s.Approved || s.Version != "1.2.3-test" {
		t.Errorf("the page is told project %q, demo %v, approved %v, version %q", s.Project, s.Demo, s.Approved, s.Version)
	}
	if strings.Contains(s.Policy, "billing.example.net") {
		t.Fatalf("the policy allows billing before anyone decided so:\n%s", s.Policy)
	}
	plain := s.Policy

	v.flip("billing")
	v.good("state", "", &s)
	if !strings.Contains(s.Policy, "billing.example.net") {
		t.Errorf("after allowing billing the policy is still:\n%s", s.Policy)
	}
	v.flip("billing")
	v.good("state", "", &s)
	if s.Policy != plain {
		t.Errorf("after taking it back the policy is:\n%s\nwant what it was:\n%s", s.Policy, plain)
	}
}

func TestFlippingASwitchThatIsNotThereIsRefused(t *testing.T) {
	v := arrive(t)
	before := v.proved()
	for _, args := range []string{`{"id":"nope"}`, `{"id":""}`, `{}`, ``, `null`, `{"id":7}`, `[]`, `not json`, `{"id":"API"}`, `{"id":"api "}`} {
		reply, status := v.call("flip", args, nil)
		if status != 422 || !reply.Refused || reply.Error != "there is no such switch" {
			t.Errorf("flip %s answered %d %q (refused %v), want it refused: there is no such switch", args, status, reply.Error, reply.Refused)
		}
	}
	if got := on(v.machine()); len(got) != 0 {
		t.Errorf("a refused flip turned on %v", got)
	}
	if diff := changed(before, v.proved()); len(diff) != 0 {
		t.Errorf("a refused flip changed %v", diff)
	}
}

func TestMoreAnswersOnlyTheDemosOwnCalls(t *testing.T) {
	v := arrive(t)
	for _, method := range []string{"machine", "flip", "reset"} {
		if _, ok, _ := v.demo.More(context.Background(), method, json.RawMessage(`{"id":"api"}`)); !ok {
			t.Errorf("the demo does not answer %q", method)
		}
	}
	for _, method := range []string{"", "nope", "Machine", "state", "remember", "verify"} {
		if got, ok, err := v.demo.More(context.Background(), method, nil); ok || got != nil || err != nil {
			t.Errorf("the demo answered %q with %v, %v, %v; that call is not its own", method, got, ok, err)
		}
	}
	if _, status := v.call("nope", "", nil); status != 404 {
		t.Errorf("a call nobody knows answered %d, want 404", status)
	}
}

// --- a visitor's own facts ---

func TestAVisitorCanRememberAndRetractInTheDemo(t *testing.T) {
	v := arrive(t)
	var got struct {
		Fact engine.View `json:"fact"`
	}
	v.good("remember", `{"claim":"The README is there","check":"file README.md"}`, &got)
	if got.Fact.Status != fact.Holds || got.Fact.By != "you" {
		t.Errorf("the visitor's fact is %s by %q, want holds by you", got.Fact.Status, got.Fact.By)
	}
	if len(v.known()) != 14 {
		t.Errorf("the page lists %d facts, want the 13 and the new one", len(v.known()))
	}
	// A claim that is not so is turned down, in the demo as anywhere.
	if reply, status := v.call("remember", `{"claim":"There is a Dockerfile","check":"file Dockerfile"}`, nil); status != 422 || !reply.Refused {
		t.Errorf("a fact whose check fails answered %d (refused %v): %s", status, reply.Refused, reply.Error)
	}
	// And a check the policy does not cover is stored, but not run.
	v.good("remember", `{"claim":"The tests pass","check":"cmd make test"}`, &got)
	if got.Fact.Status != fact.Blocked {
		t.Errorf("a command the policy does not allow is %s, want blocked", got.Fact.Status)
	}

	v.good("retract", `{"id":"`+fLegacy+`","reason":"nobody asks about it"}`, &got)
	if got.Fact.Retracted == nil || got.Fact.Retracted.By != "you" {
		t.Errorf("retracted = %+v, want it withdrawn by you", got.Fact.Retracted)
	}
	if _, still := v.known()[fLegacy]; still {
		t.Error("the retracted fact is still listed")
	}
}

func TestTwoVisitorsDoNotShareAMachine(t *testing.T) {
	a, b := arrive(t), arrive(t)
	before := b.proved()
	a.flip("api")
	a.flip("billing")
	a.good("remember", `{"claim":"The README is there","check":"file README.md"}`, nil)
	a.good("retract", `{"id":"`+fGo+`","reason":"mine alone"}`, nil)
	a.proved()

	if got := on(b.machine()); len(got) != 0 {
		t.Errorf("one visitor's flips turned on %v for another", got)
	}
	if diff := changed(before, b.proved()); len(diff) != 0 {
		t.Errorf("one visitor changed %v for another", diff)
	}
	if len(b.known()) != 13 {
		t.Errorf("the other visitor sees %d facts, want 13", len(b.known()))
	}
}

func TestTheBackupGrowsOldOnItsOwn(t *testing.T) {
	v := arrive(t)
	if now := v.proved(); now[fBackup] != fact.Holds {
		t.Fatalf("the backup fact is %s on arrival", now[fBackup])
	}
	// The backup was three hours old; a day later it is past its 26 hours,
	// and no switch was touched.
	v.clock.add(24 * time.Hour)
	if now := v.proved(); now[fBackup] != fact.Broken {
		t.Errorf("the backup fact is %s a day on, want broken: the file is 27 hours old", now[fBackup])
	}
	// Running the backup means flipping the switch there and back.
	v.flip("backup")
	v.flip("backup")
	if now := v.proved(); now[fBackup] != fact.Holds {
		t.Errorf("the backup fact is %s after the backup ran, want holds", now[fBackup])
	}
}

// --- nothing is real ---

func TestTheDemoNeverTouchesTheRealMachine(t *testing.T) {
	home, state, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("HOLDFAST_STATE", state)
	t.Chdir(cwd)

	v := arrive(t)
	// The engine proves against the pretend machine and keeps what it learns
	// in memory: there is no other world or store behind it.
	if w, ok := v.demo.Engine.World.(*sim.World); !ok || w != v.demo.World {
		t.Fatalf("the engine's world is %T, want the demo's pretend machine", v.demo.Engine.World)
	}
	if _, real := v.demo.Engine.World.(*world.OS); real {
		t.Fatal("the engine's world is the real machine")
	}
	if v.demo.Engine.Store != v.demo.Store {
		t.Fatal("the engine's store is not the demo's own, which lives in memory")
	}
	if v.api.Engine != v.demo.Engine {
		t.Fatal("the page's door does not lead to the demo's engine")
	}

	for _, s := range v.machine().Switches {
		v.flip(s.ID)
	}
	v.proved()
	// Checks that would reach far on a real machine: a command, a file that
	// exists on most, an address on the network.
	for _, args := range []string{
		`{"claim":"Docker is not running","check":"not says active systemctl is-active docker"}`,
		`{"claim":"There is a password file","check":"file /etc/passwd"}`,
		`{"claim":"Something listens on the SSH port","check":"port localhost:22"}`,
		`{"claim":"The README is there","check":"file README.md"}`,
		`{"claim":"A note"}`,
	} {
		v.call("remember", args, nil)
	}
	v.good("recall", `{"query":"docker"}`, nil)
	v.good("reset", "", nil)
	v.proved()

	for name, dir := range map[string]string{"the home folder": home, "the state folder": state, "the working folder": cwd} {
		left, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(left) != 0 {
			t.Errorf("the demo left %d files in %s, first %s", len(left), name, left[0].Name())
		}
	}
	// The pretend machine has no /etc/passwd and no SSH, whatever the real one has.
	for id, f := range v.known() {
		if strings.Contains(f.Check, "/etc/passwd") || strings.Contains(f.Check, "localhost:22") {
			if f.Status == fact.Holds {
				t.Errorf("%s (%s) holds: it was proved against the real machine", id, f.Check)
			}
		}
	}
}

// A visitor may flip switches while a proof is under way.
func TestFlippingWhileProvingIsSafe(t *testing.T) {
	v := arrive(t)
	before := v.proved()
	ids := []string{"api", "go", "billing", "backup", "flags", "docker", "legacy"}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for range 20 {
			for _, id := range ids {
				// Twice: every switch ends where it began.
				v.api.Do(context.Background(), "flip", json.RawMessage(`{"id":"`+id+`"}`))
				v.api.Do(context.Background(), "flip", json.RawMessage(`{"id":"`+id+`"}`))
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 20 {
			if _, status := v.api.Do(context.Background(), "verify", nil); status != 200 {
				t.Errorf("verify answered %d while switches were flipped", status)
			}
			v.api.Do(context.Background(), "recall", json.RawMessage(`{"query":"api"}`))
		}
	}()
	go func() {
		defer wg.Done()
		for range 20 {
			v.api.Do(context.Background(), "state", nil)
			v.api.Do(context.Background(), "machine", nil)
		}
	}()
	wg.Wait()

	if got := on(v.machine()); len(got) != 0 {
		t.Errorf("after flipping everything twice these are on: %v", got)
	}
	if diff := changed(before, v.proved()); len(diff) != 0 {
		t.Errorf("after the dust settled %v are not what they were", diff)
	}
}

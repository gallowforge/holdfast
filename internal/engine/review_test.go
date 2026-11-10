package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/sim"
	"github.com/gallowforge/holdfast/internal/store"
)

const timedOutAdvice = "TIMED OUT: the check did not finish, so this is as last known and is not proved. Run verify on it again, or confirm it yourself before relying on it."

// hang makes the machine's network stop answering, and returns a caller who
// gives up as soon as a check is waiting on it: a slow check under a short
// budget.
func (b *bench) hang() context.Context {
	b.t.Helper()
	hung := &stuck{World: b.world, started: make(chan struct{}, 1)}
	b.World = hung
	ctx, cancel := context.WithCancel(context.Background())
	b.t.Cleanup(cancel)
	go func() {
		select {
		case <-hung.started:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx
}

// A fact whose check did not finish was not proved, whatever it said last
// time. Shown as HOLDS under "Proved again just now", it would be taken as
// proved; so it is counted, labelled and explained as what it is.
func TestVerifyDoesNotCallAFactProvedWhenItsCheckDidNotFinish(t *testing.T) {
	b := newBench(t)
	v := b.remember("The API listens on port 8080", "port localhost:8080")
	b.later(time.Minute)

	got, err := b.Verify(b.hang(), nil)
	if err != nil {
		t.Fatalf("Verify = %v; a check that ran out of time is not a fault", err)
	}
	if c := got.Counts; c.Late != 1 || c.TimedOut != 1 || c.Holds != 0 || c.Total != 1 {
		t.Errorf("counts %+v; want one fact, counted as timed out and not as holding", c)
	}
	if len(got.Facts) != 1 || !got.Facts[0].TimedOut() || got.Facts[0].Status != fact.Holds {
		t.Fatalf("facts %+v; want the one fact, timed out, its status left as it was", got.Facts)
	}
	want := "Not everything could be proved again: 1 check did not finish in time. 1 fact: 1 timed out.\n\n" +
		"TIMED OUT " + v.ID + "  The API listens on port 8080\n" +
		"          not proved just now: the check did not finish in time · it held when last checked 1m ago\n" +
		"          check: port localhost:8080\n\n" +
		timedOutAdvice
	if text := got.Text(b.clock.now()); text != want {
		t.Errorf("text =\n%s\nwant\n%s", text, want)
	}

	// The count travels with the JSON, and is left out when nothing was late.
	raw, err := json.Marshal(got.Counts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"late":1`) {
		t.Errorf("the counts as JSON are %s; want late in them", raw)
	}
	b.World = b.world
	again, err := b.Verify(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(again.Counts); again.Counts.Late != 0 || strings.Contains(string(raw), "late") {
		t.Errorf("with the network back the counts are %s", raw)
	}
	if text := again.Text(b.clock.now()); !strings.HasPrefix(text, "Proved again just now. 1 fact: 1 holds.\n\nHOLDS     "+v.ID) || strings.Contains(text, "TIMED OUT") {
		t.Errorf("with the network back the text is\n%s", text)
	}
}

func TestVerifyCountsEveryCheckThatDidNotFinish(t *testing.T) {
	b := newBench(t)
	b.change(func(w *sim.World) { w.Ports["localhost:9090"] = true })
	b.remember("The API listens on port 8080", "port localhost:8080")
	b.remember("The metrics listen on port 9090", "port localhost:9090")
	b.later(time.Minute)

	got, err := b.Verify(b.hang(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := got.Counts; c.Late != 2 || c.Total != 2 || c.TimedOut != 2 || c.Holds != 0 {
		t.Fatalf("counts %+v; want two facts, both timed out and neither holding", c)
	}
	head, _, _ := strings.Cut(got.Text(b.clock.now()), "\n")
	if want := "Not everything could be proved again: 2 checks did not finish in time. 2 facts: 2 timed out."; head != want {
		t.Errorf("head = %q\nwant   %q", head, want)
	}
}

func TestRecallSaysHowManyFactsCouldNotBeCheckedAgain(t *testing.T) {
	b := newBench(t)
	v := b.remember("The API listens on port 8080", "port localhost:8080")
	b.later(time.Minute)

	r, err := b.Recall(b.hang(), "port", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := "1 fact: 1 timed out.\n\n" +
		"TIMED OUT " + v.ID + "  The API listens on port 8080\n" +
		"          not proved just now: the check did not finish in time · it held when last checked 1m ago\n" +
		"          check: port localhost:8080\n\n" +
		timedOutAdvice
	if text := r.Text(b.clock.now()); text != want {
		t.Errorf("text =\n%s\nwant\n%s", text, want)
	}

	// With the network back, nothing of the kind is said.
	b.World = b.world
	r, err = b.Recall(context.Background(), "port", 0)
	if err != nil {
		t.Fatal(err)
	}
	if text := r.Text(b.clock.now()); !strings.HasPrefix(text, "1 fact: 1 holds.\n\nHOLDS     "+v.ID) || strings.Contains(text, "in time") {
		t.Errorf("with the network back the text is\n%s", text)
	}
}

// Only a fact that would otherwise read as proved changes its label. One that
// was already broken stays BROKEN, which is the stronger warning, and is
// still counted as not checked.
func TestALateFactThatDidNotHoldKeepsItsLabel(t *testing.T) {
	at := start.Add(-4 * time.Minute)
	held := start.Add(-time.Hour)
	views := []View{{ID: "f-abc234", Claim: "The API listens on port 8080", Status: fact.Broken, Check: "port localhost:8080",
		Detail: "localhost:8080 refused the connection", CheckedAt: &at, Held: &held, Late: true}}
	if views[0].TimedOut() {
		t.Error("a broken fact is shown as timed out")
	}
	got := Verified{Facts: views, Counts: count(views)}
	if got.Counts.Late != 1 || got.Counts.Broken != 1 {
		t.Errorf("counts %+v; want one broken and one late", got.Counts)
	}
	text := got.Text(start)
	if !strings.HasPrefix(text, "Not everything could be proved again: 1 check did not finish in time. 1 fact: 1 broken.\n\nBROKEN    f-abc234") {
		t.Errorf("text =\n%s", text)
	}
	if !strings.Contains(text, "last checked 4m ago; the check did not finish in time just now") || strings.Contains(text, "TIMED OUT") {
		t.Errorf("text =\n%s\nwant the broken fact's own wording and no TIMED OUT", text)
	}
}

// behind is a store whose list is a moment out of date: what another process
// has just stored is not in it yet. By the time it is asked to add, the store
// itself knows better.
type behind struct{ store.Store }

func (behind) Facts() ([]*fact.Fact, error) { return nil, nil }

// Two agents can be told the same thing at the same moment. Each looks, sees
// nothing, and stores it; the one that comes second is told the fact is
// already there, exactly as if it had seen it when it looked.
func TestRememberFindsAFactThatWasStoredAfterItLooked(t *testing.T) {
	b := newBench(t)
	first := b.remember("The API listens on port 8080", "port localhost:8080")
	b.later(time.Minute)

	b.Engine.Store = behind{b.store}
	got, already, err := b.Remember(context.Background(), Claim{Claim: "the API listens on port 8080", Check: "port localhost:8080", By: "codex"})
	if err != nil || !already {
		t.Fatalf("Remember = %+v, %v, %v; want the fact that is there, and already", got, already, err)
	}
	if got.ID != first.ID || got.Claim != first.Claim || got.By != "claude" || got.Fresh {
		t.Errorf("Remember returned %+v; want %s as it was stored, not proved again", got, first.ID)
	}
	b.Engine.Store = b.store
	if l, err := b.List(true); err != nil || len(l.Facts)+len(l.Gone) != 1 {
		t.Errorf("the store holds %d facts (%v), want 1", len(l.Facts)+len(l.Gone), err)
	}
}

// A fact that was already broken keeps its label when its check does not
// finish, so the count cannot show it; the head line says it instead.
func TestRecallSaysWhenABrokenFactCouldNotBeCheckedAgain(t *testing.T) {
	b := newBench(t)
	b.remember("The API listens on port 8080", "port localhost:8080")
	b.change(func(w *sim.World) { w.Ports["localhost:8080"] = false })
	b.later(time.Minute)
	if r, err := b.Recall(context.Background(), "port", 0); err != nil || Count(r.Facts).Broken != 1 {
		t.Fatalf("recall = %+v, %v; want the fact broken", Count(r.Facts), err)
	}
	b.later(time.Minute)

	r, err := b.Recall(b.hang(), "port", 0)
	if err != nil {
		t.Fatal(err)
	}
	if c := Count(r.Facts); c.Broken != 1 || c.Late != 1 || c.TimedOut != 0 || c.Total != 1 {
		t.Errorf("counts %+v; want one fact, broken as before, and late", c)
	}
	head, _, _ := strings.Cut(r.Text(b.clock.now()), "\n")
	if want := "1 fact: 1 broken. 1 could not be checked again in time."; head != want {
		t.Errorf("head = %q\nwant   %q", head, want)
	}
}

// A replacement has to be checked before it takes a fact's place. A check the
// policy does not allow proves nothing, so it must not withdraw a fact that held.
func TestRememberDoesNotReplaceAFactWithOneItCouldNotCheck(t *testing.T) {
	b := newBench(t)
	old := b.remember("The API listens on port 8080", "port localhost:8080")
	before := len(b.stored())

	_, _, err := b.Remember(context.Background(), Claim{
		Claim: "The API is served from example.com", Check: "http 200 https://example.com/health", Replaces: old.ID, By: "claude",
	})
	want := "the new fact's check was not run, so it cannot replace " + old.ID +
		" yet: the policy does not allow connections to example.com. A person can allow it: holdfast allow net example.com"
	if got := refusal(t, err); got != want {
		t.Errorf("refused with\n  %s\nwant\n  %s", got, want)
	}
	if got := b.get(old.ID); !got.Live() {
		t.Error("a replacement nobody could check withdrew the fact it was meant to replace")
	}
	if n := len(b.stored()); n != before {
		t.Errorf("the refused replacement stored %d facts", n-before)
	}

	// The same fact without a replacement is stored, and waits for a person.
	v := b.remember("The API is served from example.com", "http 200 https://example.com/health")
	if v.Status != fact.Blocked {
		t.Errorf("stored on its own it is %s, want blocked", v.Status)
	}
	// A note claims no proof, so it may still take a fact's place.
	note, _, err := b.Remember(context.Background(), Claim{Claim: "The API moved and nobody wrote down where", Replaces: old.ID, By: "claude"})
	if err != nil || note.Replaces != old.ID || b.get(old.ID).Live() {
		t.Errorf("a note as a replacement: %v, replaces %q, old fact live %v", err, note.Replaces, b.get(old.ID).Live())
	}
}

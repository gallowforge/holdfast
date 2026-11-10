package engine

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/sim"
	"github.com/gallowforge/holdfast/internal/store"
)

// recall asks and fails the test on a fault.
func (b *bench) recall(query string, limit int) Recalled {
	b.t.Helper()
	r, err := b.Recall(context.Background(), query, limit)
	if err != nil {
		b.t.Fatalf("Recall(%q, %d): %v", query, limit, err)
	}
	return r
}

func (b *bench) verify(ids ...string) Verified {
	b.t.Helper()
	v, err := b.Verify(context.Background(), ids)
	if err != nil {
		b.t.Fatalf("Verify(%v): %v", ids, err)
	}
	return v
}

func TestRecallFindsTheFactsThatBearOnTheQuestion(t *testing.T) {
	b := newBench(t)
	b.remember("The API listens on port 8080", "port localhost:8080", "api")
	b.remember("The project targets Go 1.24", `contains go.mod "go 1.24"`, "build")
	b.remember("Deploys happen on Fridays", "", "process")
	b.remember("The service runs in production mode", `contains config/app.yaml "mode: production"`, "config")

	const api, goVersion, deploys, mode = "The API listens on port 8080", "The project targets Go 1.24",
		"Deploys happen on Fridays", "The service runs in production mode"
	tests := []struct {
		name  string
		query string
		want  string // the claims, best first
	}{
		{"a word of the claim", "port", api},
		{"in any case", "PORT", api},
		{"a number in the claim", "8080", api},
		{"a tag", "build", goVersion},
		{"a tag written with its hash", "#process", deploys},
		{"a word in different clothes", "deploy", deploys},
		{"a longer form of a word", "ports", api},
		{"something only the check mentions", "yaml", mode},
		{"a whole question, small words and all", "What port does the API listen on?", api},
		{"nothing that is remembered", "kubernetes", ""},
		{"words too common to mean anything, with one that does", "what is the production", mode},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := b.recall(tc.query, 0)
			if got := claims(r.Facts); got != tc.want {
				t.Errorf("Recall(%q) = %q, want %q", tc.query, got, tc.want)
			}
			if r.Known != 4 {
				t.Errorf("known = %d, want 4", r.Known)
			}
			if r.Matched != len(r.Facts) {
				t.Errorf("matched = %d with %d facts returned and no limit in the way", r.Matched, len(r.Facts))
			}
		})
	}

	for _, q := range []string{"", "   ", "?!", "the of and"} {
		if r := b.recall(q, 0); len(r.Facts) != 4 || r.Matched != 4 || r.Known != 4 {
			t.Errorf("Recall(%q) returns %d facts (matched %d, known %d); a query with no words returns everything", q, len(r.Facts), r.Matched, r.Known)
		}
	}
}

func TestRecallPutsTheBestMatchFirstAndThenWhatNeedsAttention(t *testing.T) {
	b := newBench(t)
	b.change(func(w *sim.World) { w.Ports["localhost:5432"] = true })
	b.remember("The API is written in Go", "file go.mod")
	b.remember("Port 5432 is the database", "port localhost:5432")
	b.remember("The API port is 8080", "port localhost:8080")
	b.remember("Ports below 1024 need root", "")

	// The database goes away between the remembering and the asking.
	b.later(time.Minute)
	b.change(func(w *sim.World) { w.Ports["localhost:5432"] = false })

	r := b.recall("api port", 0)
	want := "The API port is 8080 | Port 5432 is the database | The API is written in Go | Ports below 1024 need root"
	if got := claims(r.Facts); got != want {
		t.Errorf("order =\n  %s\nwant\n  %s", got, want)
	}
	if r.Facts[1].Status != fact.Broken {
		t.Errorf("the database fact is %s, want broken", r.Facts[1].Status)
	}
}

func TestRecallReturnsNoMoreThanItsLimit(t *testing.T) {
	b := newBench(t)
	const stored = 60
	for i := range stored {
		b.remember(fmt.Sprintf("Note %d about deploys", i), "")
		b.later(time.Second)
	}
	tests := []struct {
		name  string
		query string
		limit int
		want  int
	}{
		{"eight when nothing is said", "deploys", 0, DefaultLimit},
		{"twenty when everything is asked for", "", 0, 20},
		{"as many as asked", "deploys", 3, 3},
		{"as many as asked with no query", "", 5, 5},
		{"never more than fifty", "deploys", 500, MaxLimit},
		{"never more than fifty with no query", "", 500, MaxLimit},
		{"the usual number for a limit that makes no sense", "deploys", -4, DefaultLimit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := b.recall(tc.query, tc.limit)
			if len(r.Facts) != tc.want {
				t.Errorf("%d facts returned, want %d", len(r.Facts), tc.want)
			}
			if r.Matched != stored || r.Known != stored {
				t.Errorf("matched %d, known %d; the limit must not hide how many there are (%d)", r.Matched, r.Known, stored)
			}
			// Equal matches come newest first.
			for i, v := range r.Facts {
				if want := fmt.Sprintf("Note %d about deploys", stored-1-i); v.Claim != want {
					t.Fatalf("fact %d is %q, want %q", i, v.Claim, want)
				}
			}
			if !strings.Contains(r.Text(b.clock.now()), fmt.Sprintf("(showing %d of %d that match)", tc.want, stored)) {
				t.Errorf("the text does not say that it shows %d of %d:\n%s", tc.want, stored, firstLine(r.Text(b.clock.now())))
			}
		})
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func TestRecallProvesAFactAgainBeforeAnsweringWithIt(t *testing.T) {
	b := newBench(t)
	v := b.remember("The API listens on port 8080", "port localhost:8080")

	b.later(time.Minute)
	b.change(func(w *sim.World) { w.Ports["localhost:8080"] = false })
	down := b.recall("port", 0).Facts[0]
	if down.Status != fact.Broken || !down.Fresh {
		t.Fatalf("after the port closed: status %s, fresh %v; want broken, seen in this very call", down.Status, down.Fresh)
	}
	if want := "localhost:8080 does not accept connections: connection refused"; down.Detail != want {
		t.Errorf("detail = %q, want %q", down.Detail, want)
	}
	is(t, down.CheckedAt, start.Add(time.Minute), "checked_at")
	is(t, down.Since, start.Add(time.Minute), "since")
	is(t, down.Held, start, "held (when it was last true)")

	// What was seen is written down, not just reported.
	kept := b.get(v.ID)
	if kept.Last == nil || kept.Last.Status != fact.Broken || !kept.Last.At.Equal(start.Add(time.Minute)) {
		t.Errorf("the store keeps %+v", kept.Last)
	}
	if got := statusLine(kept.History); got != "holds broken" {
		t.Errorf("history = %q, want %q", got, "holds broken")
	}

	b.later(time.Minute)
	b.change(func(w *sim.World) { w.Ports["localhost:8080"] = true })
	up := b.recall("port", 0).Facts[0]
	if up.Status != fact.Holds || !up.Fresh || up.Detail != "localhost:8080 accepts connections" {
		t.Errorf("after the port opened again: %s, fresh %v, %q", up.Status, up.Fresh, up.Detail)
	}
	is(t, up.Since, start.Add(2*time.Minute), "since")
	is(t, up.Held, start.Add(2*time.Minute), "held")
	if got := statusLine(b.get(v.ID).History); got != "holds broken holds" {
		t.Errorf("history = %q", got)
	}
}

func statusLine(history []fact.Evidence) string {
	out := make([]string, len(history))
	for i, ev := range history {
		out[i] = string(ev.Status)
	}
	return strings.Join(out, " ")
}

func TestRecallTrustsEvidenceYoungerThanHalfAMinute(t *testing.T) {
	tests := []struct {
		name  string
		after time.Duration
		rerun bool
	}{
		{"in the same instant", 0, false},
		{"a second later", time.Second, false},
		{"just under half a minute later", Recent - time.Millisecond, false},
		{"exactly half a minute later", Recent, true},
		{"a minute later", time.Minute, true},
		// A clock that was set back makes the evidence look like it is from
		// the future. That is not "recent"; it is unknown, so the check runs.
		{"with evidence dated after now", -time.Hour, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t)
			b.remember("The API listens on port 8080", "port localhost:8080")
			b.later(tc.after)
			b.change(func(w *sim.World) { w.Ports["localhost:8080"] = false })

			v := b.recall("port", 0).Facts[0]
			if v.Fresh != tc.rerun {
				t.Fatalf("fresh = %v, want %v", v.Fresh, tc.rerun)
			}
			want := fact.Holds
			if tc.rerun {
				want = fact.Broken
			}
			if v.Status != want {
				t.Errorf("status = %s, want %s", v.Status, want)
			}
			if v.Late {
				t.Error("marked late, though nothing was waited for")
			}
		})
	}
}

func TestRecallNeverReturnsAWithdrawnFact(t *testing.T) {
	b := newBench(t)
	keep := b.remember("The project targets Go 1.24", `contains go.mod "go 1.24"`)
	gone := b.remember("The project has a lock file", "file go.sum")
	old := b.remember("The API listens on port 8080", "port localhost:8080")
	if _, err := b.Retract(gone.ID, "not worth keeping", "alex"); err != nil {
		t.Fatal(err)
	}
	b.change(func(w *sim.World) { w.Ports["localhost:9090"] = true })
	newer, _, err := b.Remember(context.Background(), Claim{Claim: "The API listens on port 9090", Check: "port localhost:9090", Replaces: old.ID})
	if err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{"", "project", "lock file", "port 8080"} {
		r := b.recall(q, 0)
		for _, v := range r.Facts {
			if v.ID == gone.ID || v.ID == old.ID {
				t.Errorf("Recall(%q) returned %s, which was withdrawn", q, v.ID)
			}
		}
		if r.Known != 2 {
			t.Errorf("Recall(%q): known = %d, want the 2 that are live", q, r.Known)
		}
	}
	if got := strings.Join(ids(b.recall("", 0).Facts), " "); !strings.Contains(got, keep.ID) || !strings.Contains(got, newer.ID) {
		t.Errorf("the live facts are missing from %q", got)
	}
}

func TestRecallOfAnEmptyStoreIsNotAnError(t *testing.T) {
	b := newBench(t)
	r := b.recall("anything", 0)
	if len(r.Facts) != 0 || r.Matched != 0 || r.Known != 0 {
		t.Errorf("an empty store recalled %+v", r)
	}
	if got := r.Text(start); got != "Nothing is remembered here yet." {
		t.Errorf("text = %q", got)
	}
}

func TestRecallSurvivesAQueryOfAnyLength(t *testing.T) {
	b := newBench(t)
	b.remember("The API listens on port 8080", "port localhost:8080")
	for _, q := range []string{
		strings.Repeat("é", 5000),                            // cut in the middle of a letter
		strings.Repeat("port ", 2000),                        // one word, many times
		strings.Repeat("x", MaxQuery-2) + " port",            // the word that matters lies past the cut
		"\x00\xff\xfe port \n\t",                             // not text at all
		strings.Repeat("p", MaxQuery+1),                      // just over
		strings.Repeat("\U0001F600", MaxQuery/4+1) + " port", // four-byte characters across the cut
	} {
		if _, err := b.Recall(context.Background(), q, 0); err != nil {
			t.Errorf("a query of %d bytes: %v", len(q), err)
		}
	}
	if r := b.recall(strings.Repeat("port ", 2000), 0); len(r.Facts) != 1 {
		t.Errorf("a repeated word found %d facts, want 1", len(r.Facts))
	}
}

// A check that does not answer must not hold up the answer for ever, and
// must not change what is known: the fact comes back as it was, marked late.
func TestRecallReturnsAFactAsLastKnownWhenItsCheckDoesNotFinish(t *testing.T) {
	b := newBench(t)
	v := b.remember("The API listens on port 8080", "port localhost:8080")
	b.later(time.Minute)

	hung := &stuck{World: b.world, started: make(chan struct{}, 1)}
	b.World = hung
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-hung.started
		cancel() // the caller stops waiting while the check hangs
	}()
	r, err := b.Recall(ctx, "port", 0)
	if err != nil {
		t.Fatalf("Recall = %v; a check that ran out of time is not a fault", err)
	}
	if len(r.Facts) != 1 {
		t.Fatalf("%d facts returned", len(r.Facts))
	}
	got := r.Facts[0]
	if !got.Late || got.Fresh {
		t.Errorf("late %v, fresh %v; want late and not fresh", got.Late, got.Fresh)
	}
	if got.Status != fact.Holds || got.Detail != "localhost:8080 accepts connections" {
		t.Errorf("status %s, detail %q; want what was last known", got.Status, got.Detail)
	}
	is(t, got.CheckedAt, start, "checked_at")
	if kept := b.get(v.ID); !kept.Last.At.Equal(start) || len(kept.History) != 1 {
		t.Errorf("a check that never finished changed the stored evidence: %+v", kept.Last)
	}
	want := "TIMED OUT " + v.ID + "  The API listens on port 8080\n" +
		"          not proved just now: the check did not finish in time · it held when last checked 1m ago\n" +
		"          check: port localhost:8080"
	if text := got.Text(b.clock.now()); text != want {
		t.Errorf("text =\n%s\nwant\n%s", text, want)
	}
}

func TestVerifyProvesEveryLiveFactThatHasACheck(t *testing.T) {
	b := newBench(t)
	holds := b.remember("The project targets Go 1.24", `contains go.mod "go 1.24"`)
	breaks := b.remember("The API listens on port 8080", "port localhost:8080")
	blocked := b.remember("The hosts file is in place", "file /etc/hosts")
	b.remember("Deploys happen on Fridays", "")
	gone := b.remember("The project has a lock file", "file go.sum")
	if _, err := b.Retract(gone.ID, "not worth keeping", "alex"); err != nil {
		t.Fatal(err)
	}

	// No time passes: a verify runs every check all the same.
	b.change(func(w *sim.World) { w.Ports["localhost:8080"] = false })
	v := b.verify()
	if got, want := strings.Join(ids(v.Facts), " "), breaks.ID+" "+blocked.ID+" "+holds.ID; got != want {
		t.Errorf("verified %s, want %s (what needs attention first; no notes, nothing withdrawn)", got, want)
	}
	if want := (Counts{Total: 3, Holds: 1, Broken: 1, Blocked: 1}); v.Counts != want {
		t.Errorf("counts = %+v, want %+v", v.Counts, want)
	}
	for _, f := range v.Facts {
		if !f.Fresh || f.Late {
			t.Errorf("%s: fresh %v, late %v; every check ran in this call", f.ID, f.Fresh, f.Late)
		}
	}
	if kept := b.get(breaks.ID); kept.Last.Status != fact.Broken || statusLine(kept.History) != "holds broken" || !kept.Held.Equal(start) {
		t.Errorf("the store keeps %+v, history %q, held %v", kept.Last, statusLine(kept.History), kept.Held)
	}
	if kept := b.get(gone.ID); len(kept.History) != 1 {
		t.Errorf("a retracted fact was checked again: %d entries in its history", len(kept.History))
	}
}

func TestVerifyByIDProvesExactlyTheFactsNamed(t *testing.T) {
	b := newBench(t)
	one := b.remember("The project targets Go 1.24", `contains go.mod "go 1.24"`)
	two := b.remember("The API listens on port 8080", "port localhost:8080")
	other := b.remember("The project has a lock file", "file go.sum")
	note := b.remember("Deploys happen on Fridays", "")
	b.later(time.Hour)
	b.change(func(w *sim.World) {
		w.Ports["localhost:8080"] = false
		delete(w.Files, "go.sum")
	})

	v := b.verify(" "+two.ID+" ", one.ID, two.ID, note.ID)
	if got, want := strings.Join(ids(v.Facts), " "), two.ID+" "+note.ID+" "+one.ID; got != want {
		t.Errorf("verified %s, want %s (each once, what needs attention first)", got, want)
	}
	if want := (Counts{Total: 3, Holds: 1, Broken: 1, Unproven: 1}); v.Counts != want {
		t.Errorf("counts = %+v, want %+v", v.Counts, want)
	}
	for _, f := range v.Facts {
		if wantFresh := f.ID != note.ID; f.Fresh != wantFresh {
			t.Errorf("%s: fresh = %v, want %v (a note has nothing to run)", f.ID, f.Fresh, wantFresh)
		}
	}
	// The fact that was not named was not checked, though it would now fail.
	if kept := b.get(other.ID); kept.Last.Status != fact.Holds || !kept.Last.At.Equal(start) {
		t.Errorf("a fact nobody named was checked: %+v", kept.Last)
	}
}

func TestVerifyRefusesAnIDItDoesNotKnowAndChecksNothing(t *testing.T) {
	b := newBench(t)
	known := b.remember("The API listens on port 8080", "port localhost:8080")
	b.later(time.Hour)
	b.change(func(w *sim.World) { w.Ports["localhost:8080"] = false })

	for _, tc := range []struct{ id, shown string }{
		{"f-222222", "f-222222"},
		{"nonsense", "nonsense"},
		{"", ""},
		{"  f-999999\n", "f-999999"},
	} {
		_, err := b.Verify(context.Background(), []string{known.ID, tc.id})
		if got, want := refusal(t, err), fmt.Sprintf("there is no fact with the id %q", tc.shown); got != want {
			t.Errorf("Verify(%q) refused with %q, want %q", tc.id, got, want)
		}
	}
	if kept := b.get(known.ID); kept.Last.Status != fact.Holds || !kept.Last.At.Equal(start) {
		t.Errorf("the fact that was named alongside was checked anyway: %+v", kept.Last)
	}
}

func TestVerifyWithNothingToProve(t *testing.T) {
	b := newBench(t)
	v := b.verify()
	if len(v.Facts) != 0 || v.Counts != (Counts{}) {
		t.Errorf("an empty store verified %+v", v)
	}
	if got := v.Text(start); got != "There are no facts with checks to prove." {
		t.Errorf("text = %q", got)
	}
	// Notes alone are nothing to prove either.
	b.remember("Deploys happen on Fridays", "")
	if got := b.verify().Text(start); got != "There are no facts with checks to prove." {
		t.Errorf("with only a note, text = %q", got)
	}
}

// What a person allows afterwards is what a blocked fact was waiting for.
func TestABlockedFactIsProvedOnceThePolicyAllowsItsCheck(t *testing.T) {
	b := newBench(t)
	b.change(func(w *sim.World) {
		w.Ports["db.internal:5432"] = true
		w.Names["db.internal"] = []netip.Addr{netip.MustParseAddr("10.0.0.5")}
	})
	v := b.remember("The database accepts connections", "port db.internal:5432")
	if v.Status != fact.Blocked {
		t.Fatalf("status = %s, want blocked", v.Status)
	}
	b.later(time.Minute)
	if again := b.verify(v.ID).Facts[0]; again.Status != fact.Blocked || !again.Fresh {
		t.Errorf("before anything was allowed: %s, fresh %v", again.Status, again.Fresh)
	}

	b.store.SetPolicy(rules(t, benchPolicy+"net db.internal\n"))
	b.later(time.Minute)
	now := b.verify(v.ID).Facts[0]
	if now.Status != fact.Holds || now.Detail != "db.internal:5432 accepts connections" {
		t.Errorf("after the policy allowed it: %s, %q", now.Status, now.Detail)
	}
	is(t, now.Held, start.Add(2*time.Minute), "held")
	if got := statusLine(b.get(v.ID).History); got != "blocked holds" {
		t.Errorf("history = %q, want %q", got, "blocked holds")
	}
}

// More checks than workers, all at once, under the race detector.
func TestVerifyRunsManyChecksAtOnceAndLosesNone(t *testing.T) {
	b := newBench(t)
	const n = 5 * Workers
	for i := range n {
		name := fmt.Sprintf("notes/%02d.txt", i)
		b.change(func(w *sim.World) { w.Write(name, "kept\n", start) })
		b.remember(fmt.Sprintf("File %d is kept", i), "file "+name)
	}
	b.later(time.Hour)
	b.change(func(w *sim.World) {
		for i := 0; i < n; i += 2 {
			delete(w.Files, fmt.Sprintf("notes/%02d.txt", i))
		}
	})
	v := b.verify()
	if want := (Counts{Total: n, Holds: n / 2, Broken: n / 2}); v.Counts != want {
		t.Fatalf("counts = %+v, want %+v", v.Counts, want)
	}
	seen := map[string]bool{}
	for i, f := range v.Facts {
		if seen[f.ID] || !f.Fresh {
			t.Errorf("%s: seen twice %v, fresh %v", f.ID, seen[f.ID], f.Fresh)
		}
		seen[f.ID] = true
		if want := i < n/2; (f.Status == fact.Broken) != want {
			t.Fatalf("fact %d is %s; the broken half comes first", i, f.Status)
		}
	}
	// And the store agrees with the answer.
	l, err := b.List(false)
	if err != nil {
		t.Fatal(err)
	}
	if l.Counts != v.Counts {
		t.Errorf("the store counts %+v after a verify that reported %+v", l.Counts, v.Counts)
	}
}

// noted is a store whose policy file is not the one in force.
type noted struct {
	store.Store
	note string
}

func (n noted) Policy() (*policy.Policy, string) {
	p, _ := n.Store.Policy()
	return p, n.note
}

func TestEveryAnswerCarriesTheNoteAboutThePolicy(t *testing.T) {
	const note = "the policy file was changed and is not approved on this machine; the last approved policy is in force"
	b := newBench(t)
	b.remember("The API listens on port 8080", "port localhost:8080")
	b.Store = noted{b.store, note}

	r := b.recall("port", 0)
	v := b.verify()
	l, err := b.List(false)
	if err != nil {
		t.Fatal(err)
	}
	for what, got := range map[string]string{"Recall": r.Note, "Verify": v.Note, "List": l.Note} {
		if got != note {
			t.Errorf("%s carries the note %q", what, got)
		}
	}
	now := b.clock.now()
	for what, text := range map[string]string{"Recall": r.Text(now), "Verify": v.Text(now), "List": l.Text(now)} {
		if !strings.HasSuffix(text, "\n\n"+note) {
			t.Errorf("the text of %s does not end with the note:\n%s", what, text)
		}
	}
}

package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
)

// t0 is the moment every test starts from. The stores never look at a clock:
// every time they keep is one a caller handed them.
var t0 = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

// maker opens a store for one test. The second function closes it and opens
// it again the way a new process would; for a store that lives in memory it
// returns the same store.
type maker func(t *testing.T) (s Store, reopen func() Store)

// kinds lists every kind of store, so one promise is tested against all of
// them. The directory store adds itself in dir_test.go.
var kinds = map[string]maker{
	"memory": func(t *testing.T) (Store, func() Store) {
		m := NewMemory(policy.Default())
		return m, func() Store { return m }
	},
}

func eachStore(t *testing.T, fn func(t *testing.T, s Store, reopen func() Store)) {
	t.Helper()
	for _, name := range []string{"memory", "dir"} {
		mk, ok := kinds[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			s, reopen := mk(t)
			fn(t, s, reopen)
		})
	}
}

func parse(t testing.TB, check string) *fact.Check {
	t.Helper()
	c, err := fact.ParseCheck(check)
	if err != nil {
		t.Fatalf("the test's own check %q does not parse: %v", check, err)
	}
	return c
}

func note(claim string, created time.Time) *fact.Fact {
	return &fact.Fact{Claim: claim, By: "tester", Created: created}
}

func checked(t testing.TB, claim, check string, created time.Time) *fact.Fact {
	f := note(claim, created)
	f.Check = parse(t, check)
	return f
}

func evidence(status fact.Status, when time.Time) fact.Evidence {
	return fact.Evidence{Status: status, Detail: "seen " + string(status), At: when, Ms: 3}
}

func mustAdd(t testing.TB, s Store, f *fact.Fact, ev *fact.Evidence) *fact.Fact {
	t.Helper()
	got, err := s.Add(f, ev)
	if err != nil {
		t.Fatalf("Add(%q): %v", f.Claim, err)
	}
	return got
}

func mustGet(t testing.TB, s Store, id string) *fact.Fact {
	t.Helper()
	f, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return f
}

func mustFacts(t testing.TB, s Store) []*fact.Fact {
	t.Helper()
	all, err := s.Facts()
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	return all
}

func mustRecord(t testing.TB, s Store, id string, ev fact.Evidence) {
	t.Helper()
	if err := s.Record([]Proof{{ID: id, Ev: ev}}); err != nil {
		t.Fatalf("Record(%s, %s): %v", id, ev.Status, err)
	}
}

// asJSON writes facts the way the journal and the interfaces do, so two
// states can be compared in one line and a difference can be read.
func asJSON(t testing.TB, v any) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	return b.String()
}

func statuses(history []fact.Evidence) string {
	parts := make([]string, len(history))
	for i, ev := range history {
		parts[i] = string(ev.Status)
	}
	return strings.Join(parts, " ")
}

func TestAddGivesTheFactAnIdAndKeepsWhatWasClaimed(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, reopen func() Store) {
		in := checked(t, "the API listens on port 8080", "port localhost:8080", t0)
		in.Tags = []string{"api", "ports"}
		in.TTL = fact.Seconds(3600)
		got := mustAdd(t, s, in, nil)

		if !fact.ValidID(got.ID) {
			t.Fatalf("the new fact's id is %q, which is not a fact id", got.ID)
		}
		for _, s := range []Store{s, reopen()} {
			f := mustGet(t, s, got.ID)
			if f.Claim != in.Claim || f.By != "tester" || !f.Created.Equal(t0) || f.TTL != 3600 {
				t.Errorf("stored fact = %+v, want what was claimed", f)
			}
			if strings.Join(f.Tags, ",") != "api,ports" {
				t.Errorf("tags = %v, want [api ports]", f.Tags)
			}
			if f.Check == nil || f.Check.String() != "port localhost:8080" {
				t.Errorf("check = %v, want port localhost:8080", f.Check)
			}
			if f.Last != nil || !f.Since.IsZero() || !f.Held.IsZero() || len(f.History) != 0 {
				t.Errorf("a fact added without evidence has proof: %+v", f)
			}
			if !f.Live() {
				t.Error("a new fact is not live")
			}
		}
	})
}

func TestAddNeverTrustsProofOrStandingTheCallerSupplies(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, reopen func() Store) {
		in := checked(t, "the lock file is in place", "file go.sum", t0)
		last := evidence(fact.Holds, at(time.Hour))
		in.Last, in.Since, in.Held = &last, at(time.Hour), at(time.Hour)
		in.History = []fact.Evidence{last}
		in.Vouched, in.VouchedBy = at(time.Hour), "nobody"
		in.Retracted = &fact.Retraction{At: at(time.Hour), By: "nobody"}
		in.ReplacedBy = "f-222222"
		in.ID = "f-333333"

		got := mustAdd(t, s, in, nil)
		for _, s := range []Store{s, reopen()} {
			f := mustGet(t, s, got.ID)
			if f.ID == "f-333333" {
				t.Error("the store kept an id the caller chose")
			}
			if f.Last != nil || len(f.History) != 0 || !f.Since.IsZero() || !f.Held.IsZero() {
				t.Errorf("proof came in with the fact: %+v", f)
			}
			if !f.Vouched.IsZero() || f.VouchedBy != "" || f.Retracted != nil || f.ReplacedBy != "" {
				t.Errorf("standing came in with the fact: %+v", f)
			}
		}
	})
}

func TestGetOfAnUnknownIdIsNotFound(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, _ func() Store) {
		mustAdd(t, s, note("something is so", t0), nil)
		for _, id := range []string{"f-222222", "", "nonsense"} {
			if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
				t.Errorf("Get(%q) = %v, want ErrNotFound", id, err)
			}
		}
	})
}

func TestFactsAreListedOldestFirstRetractedOnesIncluded(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, reopen func() Store) {
		var ids []string
		for i := range 5 {
			f := mustAdd(t, s, note(fmt.Sprintf("note number %d", i), at(time.Duration(i)*time.Minute)), nil)
			ids = append(ids, f.ID)
		}
		if err := s.Retract(ids[1], fact.Retraction{At: at(time.Hour), By: "tester", Reason: "wrong"}); err != nil {
			t.Fatal(err)
		}
		for _, s := range []Store{s, reopen()} {
			all := mustFacts(t, s)
			if len(all) != 5 {
				t.Fatalf("%d facts listed, want 5", len(all))
			}
			for i, f := range all {
				if f.ID != ids[i] {
					t.Errorf("fact %d is %s, want %s (the order they were added in)", i, f.ID, ids[i])
				}
			}
			if all[1].Retracted == nil {
				t.Error("the retracted fact is listed without its retraction")
			}
		}
	})
}

func TestAnEmptyStoreListsNothing(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, _ func() Store) {
		if all := mustFacts(t, s); len(all) != 0 {
			t.Errorf("an empty store lists %d facts", len(all))
		}
	})
}

func TestTheFirstEvidenceComesInWithTheFact(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, reopen func() Store) {
		ev := evidence(fact.Holds, at(time.Second))
		got := mustAdd(t, s, checked(t, "go.mod is there", "file go.mod", t0), &ev)
		for _, s := range []Store{s, reopen()} {
			f := mustGet(t, s, got.ID)
			if f.Last == nil || f.Last.Status != fact.Holds || f.Last.Detail != ev.Detail || f.Last.Ms != 3 {
				t.Fatalf("last evidence = %+v, want what Add was given", f.Last)
			}
			if !f.Since.Equal(ev.At) || !f.Held.Equal(ev.At) || !f.Last.At.Equal(ev.At) {
				t.Errorf("since %v, held %v, last %v: all three want %v", f.Since, f.Held, f.Last.At, ev.At)
			}
			if statuses(f.History) != "holds" {
				t.Errorf("history = %q, want one entry", statuses(f.History))
			}
		}
	})
}

// The rule for evidence, in one place: a change of status starts a new state
// (Since) and is added to the history; a check that passes moves Held; the
// last evidence is always kept.
func TestEvidenceMovesSinceOnlyOnAChangeOfStatus(t *testing.T) {
	type step struct {
		status fact.Status
		after  time.Duration
	}
	tests := []struct {
		name        string
		steps       []step
		wantSince   time.Duration
		wantHeld    time.Duration // -1: never held
		wantHistory string
	}{
		{
			name:        "one passing check",
			steps:       []step{{fact.Holds, time.Minute}},
			wantSince:   time.Minute,
			wantHeld:    time.Minute,
			wantHistory: "holds",
		},
		{
			name:        "passing again changes nothing but the last check and last held",
			steps:       []step{{fact.Holds, time.Minute}, {fact.Holds, 2 * time.Minute}, {fact.Holds, 3 * time.Minute}},
			wantSince:   time.Minute,
			wantHeld:    3 * time.Minute,
			wantHistory: "holds",
		},
		{
			name:        "breaking starts a new state and keeps when it last held",
			steps:       []step{{fact.Holds, time.Minute}, {fact.Holds, 2 * time.Minute}, {fact.Broken, 3 * time.Minute}, {fact.Broken, 4 * time.Minute}},
			wantSince:   3 * time.Minute,
			wantHeld:    2 * time.Minute,
			wantHistory: "holds broken",
		},
		{
			name:        "a fact that never passed has never held",
			steps:       []step{{fact.Blocked, time.Minute}, {fact.Broken, 2 * time.Minute}},
			wantSince:   2 * time.Minute,
			wantHeld:    -1,
			wantHistory: "blocked broken",
		},
		{
			name:        "recovering is a change too",
			steps:       []step{{fact.Holds, time.Minute}, {fact.Broken, 2 * time.Minute}, {fact.Blocked, 3 * time.Minute}, {fact.Holds, 4 * time.Minute}, {fact.Holds, 5 * time.Minute}},
			wantSince:   4 * time.Minute,
			wantHeld:    5 * time.Minute,
			wantHistory: "holds broken blocked holds",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, s Store, _ func() Store) {
				f := mustAdd(t, s, checked(t, "go.mod is there", "file go.mod", t0), nil)
				var last fact.Evidence
				for _, st := range tc.steps {
					last = evidence(st.status, at(st.after))
					mustRecord(t, s, f.ID, last)
				}
				got := mustGet(t, s, f.ID)
				if !got.Since.Equal(at(tc.wantSince)) {
					t.Errorf("since = %v, want %v", got.Since.Sub(t0), tc.wantSince)
				}
				if tc.wantHeld < 0 {
					if !got.Held.IsZero() {
						t.Errorf("held = %v, want never", got.Held)
					}
				} else if !got.Held.Equal(at(tc.wantHeld)) {
					t.Errorf("held = %v, want %v", got.Held.Sub(t0), tc.wantHeld)
				}
				if got.Last == nil || got.Last.Status != last.Status || !got.Last.At.Equal(last.At) {
					t.Errorf("last = %+v, want the evidence recorded last (%s at %v)", got.Last, last.Status, last.At)
				}
				if statuses(got.History) != tc.wantHistory {
					t.Errorf("history = %q, want %q", statuses(got.History), tc.wantHistory)
				}
				if n := len(got.History); n > 0 && !got.History[n-1].At.Equal(got.Since) {
					t.Errorf("the last history entry is from %v but the state began at %v", got.History[n-1].At, got.Since)
				}
			})
		})
	}
}

func TestHistoryKeepsOnlyTheLatestChanges(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, reopen func() Store) {
		f := mustAdd(t, s, checked(t, "go.mod is there", "file go.mod", t0), nil)
		const flips = fact.MaxHistory + 15
		for i := range flips {
			status := fact.Holds
			if i%2 == 1 {
				status = fact.Broken
			}
			mustRecord(t, s, f.ID, evidence(status, at(time.Duration(i+1)*time.Hour)))
		}
		for _, s := range []Store{s, reopen()} {
			got := mustGet(t, s, f.ID)
			if len(got.History) != fact.MaxHistory {
				t.Fatalf("history holds %d entries, want the cap of %d", len(got.History), fact.MaxHistory)
			}
			oldest, newest := got.History[0], got.History[len(got.History)-1]
			if want := at(time.Duration(flips-fact.MaxHistory+1) * time.Hour); !oldest.At.Equal(want) {
				t.Errorf("the oldest entry kept is from %v, want %v: the cap must drop the oldest", oldest.At, want)
			}
			if want := at(flips * time.Hour); !newest.At.Equal(want) {
				t.Errorf("the newest entry is from %v, want %v", newest.At, want)
			}
		}
	})
}

func TestRecordIgnoresNotesAndIdsNobodyHas(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, reopen func() Store) {
		n := mustAdd(t, s, note("the team deploys on Fridays", t0), nil)
		c := mustAdd(t, s, checked(t, "go.mod is there", "file go.mod", t0), nil)
		err := s.Record([]Proof{
			{ID: n.ID, Ev: evidence(fact.Holds, at(time.Minute))},
			{ID: "f-222222", Ev: evidence(fact.Holds, at(time.Minute))},
			{ID: c.ID, Ev: evidence(fact.Broken, at(time.Minute))},
		})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
		if err := s.Record(nil); err != nil {
			t.Fatalf("Record of nothing: %v", err)
		}
		for _, s := range []Store{s, reopen()} {
			if f := mustGet(t, s, n.ID); f.Last != nil || len(f.History) != 0 {
				t.Errorf("a fact without a check was given proof: %+v", f.Last)
			}
			if f := mustGet(t, s, c.ID); f.Last == nil || f.Last.Status != fact.Broken {
				t.Errorf("the evidence next to the ignored ones was lost: %+v", f.Last)
			}
			if all := mustFacts(t, s); len(all) != 2 {
				t.Errorf("%d facts after Record, want 2", len(all))
			}
		}
	})
}

func TestRetractKeepsWhoAndWhyAndTheFirstRetractionWins(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, reopen func() Store) {
		f := mustAdd(t, s, note("staging runs the old schema", t0), nil)
		first := fact.Retraction{At: at(time.Hour), By: "alex", Reason: "it was migrated"}
		if err := s.Retract(f.ID, first); err != nil {
			t.Fatalf("Retract: %v", err)
		}
		if err := s.Retract(f.ID, fact.Retraction{At: at(2 * time.Hour), By: "someone else", Reason: "another reason"}); err != nil {
			t.Fatalf("a second Retract: %v", err)
		}
		for _, s := range []Store{s, reopen()} {
			got := mustGet(t, s, f.ID)
			if got.Retracted == nil {
				t.Fatal("the fact is not retracted")
			}
			if r := *got.Retracted; !r.At.Equal(first.At) || r.By != "alex" || r.Reason != "it was migrated" {
				t.Errorf("retraction = %+v, want the first one", r)
			}
			if got.Live() {
				t.Error("a retracted fact still counts as live")
			}
		}
		if err := s.Retract("f-222222", first); !errors.Is(err, ErrNotFound) {
			t.Errorf("Retract of an unknown id = %v, want ErrNotFound", err)
		}
	})
}

func TestVouchKeepsTheLatestVoucher(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, reopen func() Store) {
		f := mustAdd(t, s, note("the release branch is cut on Mondays", t0), nil)
		for _, v := range []struct {
			by    string
			after time.Duration
		}{{"maria", 2 * time.Hour}, {"sam", 3 * time.Hour}, {"late arrival", time.Hour}} {
			if err := s.Vouch(f.ID, v.by, at(v.after)); err != nil {
				t.Fatalf("Vouch by %s: %v", v.by, err)
			}
		}
		for _, s := range []Store{s, reopen()} {
			got := mustGet(t, s, f.ID)
			if got.VouchedBy != "sam" || !got.Vouched.Equal(at(3*time.Hour)) {
				t.Errorf("vouched by %q at %v, want the latest: sam at %v", got.VouchedBy, got.Vouched, at(3*time.Hour))
			}
			if !got.NotedAt().Equal(at(3 * time.Hour)) {
				t.Errorf("the note's age counts from %v, want from the vouching", got.NotedAt())
			}
		}
		if err := s.Vouch("f-222222", "alex", at(time.Hour)); !errors.Is(err, ErrNotFound) {
			t.Errorf("Vouch of an unknown id = %v, want ErrNotFound", err)
		}
	})
}

// The clock is kept to the millisecond, so two people can vouch at the same
// moment; the one who spoke last is the one recorded.
func TestVouchAtTheSameMomentKeepsTheOneWhoSpokeLast(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, reopen func() Store) {
		f := mustAdd(t, s, note("the release branch is cut on Mondays", t0), nil)
		for _, by := range []string{"maria", "sam"} {
			if err := s.Vouch(f.ID, by, at(time.Hour)); err != nil {
				t.Fatalf("Vouch by %s: %v", by, err)
			}
		}
		for _, s := range []Store{s, reopen()} {
			if got := mustGet(t, s, f.ID); got.VouchedBy != "sam" || !got.Vouched.Equal(at(time.Hour)) {
				t.Errorf("vouched by %q at %v, want sam, who spoke last", got.VouchedBy, got.Vouched)
			}
		}
	})
}

func TestReplacingAFactMarksTheOldOne(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, reopen func() Store) {
		old := mustAdd(t, s, note("the API listens on port 8080", t0), nil)
		in := note("the API listens on port 9090", at(time.Hour))
		in.Replaces = old.ID
		next := mustAdd(t, s, in, nil)
		if next.Replaces != old.ID {
			t.Errorf("the new fact replaces %q, want %s", next.Replaces, old.ID)
		}
		for _, s := range []Store{s, reopen()} {
			was := mustGet(t, s, old.ID)
			if was.ReplacedBy != next.ID {
				t.Errorf("the old fact is replaced by %q, want %s", was.ReplacedBy, next.ID)
			}
			if was.Live() {
				t.Error("a replaced fact still counts as live")
			}
			if now := mustGet(t, s, next.ID); !now.Live() || now.ReplacedBy != "" {
				t.Errorf("the replacement is not live: %+v", now)
			}
		}
	})
}

func TestReplacingIsRefusedWhenThereIsNothingLiveToReplace(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, reopen func() Store) {
		replaced := mustAdd(t, s, note("the first claim", t0), nil)
		in := note("the second claim", t0)
		in.Replaces = replaced.ID
		second := mustAdd(t, s, in, nil)
		retracted := mustAdd(t, s, note("a claim that is withdrawn", t0), nil)
		if err := s.Retract(retracted.ID, fact.Retraction{At: t0, Reason: "wrong"}); err != nil {
			t.Fatal(err)
		}
		before := len(mustFacts(t, s))

		tests := []struct {
			name     string
			replaces string
			want     string
			notFound bool
		}{
			{"an id nobody has", "f-222222", "f-222222 cannot be replaced: there is no fact with that id", true},
			{"a fact already replaced", replaced.ID, replaced.ID + " was already replaced by " + second.ID, false},
			{"a fact already retracted", retracted.ID, retracted.ID + " was already retracted", false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				in := note("a third claim", t0)
				in.Replaces = tc.replaces
				_, err := s.Add(in, nil)
				if err == nil {
					t.Fatal("the fact was added")
				}
				if err.Error() != tc.want {
					t.Errorf("error = %q, want %q", err, tc.want)
				}
				var rejected *Rejected
				if tc.notFound != errors.Is(err, ErrNotFound) || tc.notFound == errors.As(err, &rejected) {
					t.Errorf("error %v: ErrNotFound is %v, Rejected is %v", err, errors.Is(err, ErrNotFound), errors.As(err, &rejected))
				}
			})
		}
		for _, s := range []Store{s, reopen()} {
			if after := len(mustFacts(t, s)); after != before {
				t.Errorf("%d facts after the refusals, want %d: a refused fact must leave nothing behind", after, before)
			}
		}
	})
}

func TestWhatAStoreReturnsIsACopy(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, _ func() Store) {
		ev := evidence(fact.Holds, at(time.Second))
		in := checked(t, "go.mod is there", "file go.mod", t0)
		in.Tags = []string{"build"}
		added := mustAdd(t, s, in, &ev)

		spoil := func(f *fact.Fact) {
			f.Claim = "changed behind the store's back"
			f.Tags[0] = "spoiled"
			f.Check.Path = "elsewhere"
			f.Last.Status = fact.Broken
			f.History[0].Status = fact.Broken
		}
		spoil(added)
		spoil(mustGet(t, s, added.ID))
		spoil(mustFacts(t, s)[0])
		// The caller's own fact is not the store's either.
		in.Claim, in.Tags[0], in.Check.Path = "changed after adding", "spoiled", "elsewhere"

		got := mustGet(t, s, added.ID)
		if got.Claim != "go.mod is there" || got.Tags[0] != "build" || got.Check.Path != "go.mod" {
			t.Errorf("the stored fact changed through a copy: %+v", got)
		}
		if got.Last.Status != fact.Holds || got.History[0].Status != fact.Holds {
			t.Errorf("the stored proof changed through a copy: last %+v, history %+v", got.Last, got.History)
		}
	})
}

func TestIdsAreUniqueAndAFullSpaceIsAnErrorNotALoop(t *testing.T) {
	m := NewMemory(policy.Default())
	m.Rand = bytes.NewReader(make([]byte, 16*70)) // the same id over and over
	first := mustAdd(t, m, note("the first", t0), nil)
	if first.ID != "f-222222" {
		t.Fatalf("with all-zero randomness the id is %q, want f-222222", first.ID)
	}
	if _, err := m.Add(note("the second", t0), nil); err == nil {
		t.Fatal("a second fact got the id of the first")
	}
	if all := mustFacts(t, m); len(all) != 1 {
		t.Errorf("%d facts stored, want 1", len(all))
	}
}

func TestAStoreHoldsAtMost2000LiveFacts(t *testing.T) {
	m := NewMemory(policy.Default())
	var ids []string
	for i := range MaxFacts {
		ids = append(ids, mustAdd(t, m, note(fmt.Sprintf("fact number %d", i), t0), nil).ID)
	}
	_, err := m.Add(note("one too many", t0), nil)
	var rejected *Rejected
	if !errors.As(err, &rejected) {
		t.Fatalf("adding fact %d = %v, want a Rejected", MaxFacts+1, err)
	}
	if want := "this store already holds 2000 facts; retract some before adding more"; rejected.Why != want {
		t.Errorf("reason = %q, want %q", rejected.Why, want)
	}

	// Only live facts count: retracting one makes room for one.
	if err := m.Retract(ids[0], fact.Retraction{At: t0, Reason: "making room"}); err != nil {
		t.Fatal(err)
	}
	mustAdd(t, m, note("fits after a retraction", t0), nil)
	if _, err := m.Add(note("one too many again", t0), nil); !errors.As(err, &rejected) {
		t.Errorf("the store took more than %d live facts: %v", MaxFacts, err)
	}
	if all := mustFacts(t, m); len(all) != MaxFacts+1 {
		t.Errorf("%d facts in all, want %d (the retracted one is kept)", len(all), MaxFacts+1)
	}
}

func TestMemoryPolicyIsTheOneItWasGivenAndNeverNeedsApproval(t *testing.T) {
	rules := policy.Default()
	m := NewMemory(rules)
	if p, why := m.Policy(); p != rules || why != "" {
		t.Errorf("Policy() = %v, %q; want the rules given and no note", p, why)
	}
	wider := &policy.Policy{Files: []string{"."}, Net: []policy.NetRule{{Class: "public"}}}
	m.SetPolicy(wider)
	if p, why := m.Policy(); p != wider || why != "" {
		t.Errorf("after SetPolicy, Policy() = %v, %q", p, why)
	}
}

func TestSeedPlaysAPastIntoAMemoryStore(t *testing.T) {
	m := NewMemory(policy.Default())
	c := parse(t, "port localhost:8080")
	m.Seed(
		Event{T: t0, Op: "add", ID: "f-7k2m9x", Fact: &fact.Fact{Claim: "the API listens on 8080", Check: c, By: "claude"}},
		Event{T: at(time.Minute), Op: "evidence", ID: "f-7k2m9x", Ev: &fact.Evidence{Status: fact.Holds, Detail: "it listens", At: at(time.Minute)}},
		Event{T: at(time.Hour), Op: "add", ID: "f-c4hd8p", Fact: &fact.Fact{Claim: "a note", By: "alex"}},
		Event{T: at(2 * time.Hour), Op: "vouch", ID: "f-c4hd8p", By: "alex"},
	)
	api := mustGet(t, m, "f-7k2m9x")
	if api.Last == nil || api.Last.Status != fact.Holds || !api.Held.Equal(at(time.Minute)) {
		t.Errorf("seeded evidence did not arrive: %+v", api)
	}
	if !api.Created.Equal(t0) {
		t.Errorf("a seeded fact with no creation time was created at %v, want the time of its event", api.Created)
	}
	if n := mustGet(t, m, "f-c4hd8p"); n.VouchedBy != "alex" {
		t.Errorf("seeded vouching did not arrive: %+v", n)
	}
}

func TestSeedPanicsOnAnEventThatMakesNoSense(t *testing.T) {
	tests := []struct {
		name  string
		event Event
	}{
		{"evidence for a fact nobody has", Event{T: t0, Op: "evidence", ID: "f-222222", Ev: &fact.Evidence{Status: fact.Holds, At: t0}}},
		{"an id that is not an id", Event{T: t0, Op: "add", ID: "seven", Fact: &fact.Fact{Claim: "a note"}}},
		{"a fact with no claim", Event{T: t0, Op: "add", ID: "f-222222", Fact: &fact.Fact{}}},
		{"an add with no fact", Event{T: t0, Op: "add", ID: "f-222222"}},
		{"a check that is not well formed", Event{T: t0, Op: "add", ID: "f-222222", Fact: &fact.Fact{Claim: "a note", Check: &fact.Check{Kind: "teleport"}}}},
		{"an unknown kind of event", Event{T: t0, Op: "celebrate", ID: "f-222222"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				got := fmt.Sprint(recover())
				if want := "store: bad seed event " + tc.event.Op + " " + tc.event.ID; got != want {
					t.Errorf("panic = %q, want %q", got, want)
				}
			}()
			NewMemory(policy.Default()).Seed(tc.event)
		})
	}
}

// What apply accepts decides what a journal line may do, whoever wrote it.
func TestApplyRefusesEventsThatMakeNoSense(t *testing.T) {
	add := Event{T: t0, Op: "add", ID: "f-abc234", Fact: &fact.Fact{Claim: "a checked fact", Check: &fact.Check{Kind: "file", Path: "go.mod"}}}
	noted := Event{T: t0, Op: "add", ID: "f-def567", Fact: &fact.Fact{Claim: "a note"}}
	tests := []struct {
		name  string
		event Event
		want  bool
	}{
		{"the same id twice", add, false},
		{"evidence with a status that is not proof", Event{Op: "evidence", ID: "f-abc234", Ev: &fact.Evidence{Status: fact.Unproven, At: t0}}, false},
		{"evidence with a made-up status", Event{Op: "evidence", ID: "f-abc234", Ev: &fact.Evidence{Status: "fine", At: t0}}, false},
		{"evidence with nothing in it", Event{Op: "evidence", ID: "f-abc234"}, false},
		{"evidence for a note", Event{Op: "evidence", ID: "f-def567", Ev: &fact.Evidence{Status: fact.Holds, At: t0}}, false},
		{"a retraction of nothing", Event{Op: "retract", ID: "f-222222"}, false},
		{"a vouching for nothing", Event{Op: "vouch", ID: "f-222222"}, false},
		{"good evidence", Event{Op: "evidence", ID: "f-abc234", Ev: &fact.Evidence{Status: fact.Blocked, At: t0}}, true},
		{"a retraction", Event{T: t0, Op: "retract", ID: "f-def567", Reason: "wrong"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newState()
			if !s.apply(add) || !s.apply(noted) {
				t.Fatal("the two good facts were not accepted")
			}
			if got := s.apply(tc.event); got != tc.want {
				t.Errorf("apply = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAJournalLineCannotCarryProofInsideItsFact(t *testing.T) {
	s := newState()
	last := evidence(fact.Holds, t0)
	ok := s.apply(Event{T: t0, Op: "add", ID: "f-abc234", Fact: &fact.Fact{
		ID: "f-zzzzzz", Claim: "  a claim\twith   odd spacing ", Check: &fact.Check{Kind: "file", Path: "go.mod"},
		Last: &last, Held: t0, Since: t0, History: []fact.Evidence{last},
		Retracted: &fact.Retraction{At: t0}, ReplacedBy: "f-def567", Vouched: t0, VouchedBy: "nobody",
	}})
	if !ok {
		t.Fatal("the line was not accepted at all")
	}
	f := s.facts["f-abc234"]
	if f == nil || s.facts["f-zzzzzz"] != nil {
		t.Fatal("the fact was stored under the id inside it, not the id of its event")
	}
	if f.Claim != "a claim with odd spacing" {
		t.Errorf("claim = %q, want it cleaned to one plain line", f.Claim)
	}
	if f.Last != nil || len(f.History) != 0 || !f.Held.IsZero() || !f.Since.IsZero() || f.Retracted != nil || f.ReplacedBy != "" || f.VouchedBy != "" {
		t.Errorf("proof or standing came from inside the fact: %+v", f)
	}
}

func TestMemoryIsSafeForManyGoroutines(t *testing.T) {
	m := NewMemory(policy.Default())
	const workers, each = 8, 40
	check := parse(t, "file go.mod")
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				f, err := m.Add(&fact.Fact{Claim: fmt.Sprintf("worker %d fact %d", w, i), Check: check.Clone(), By: "tester", Created: t0}, nil)
				if err != nil {
					t.Errorf("Add: %v", err)
					return
				}
				m.Record([]Proof{{ID: f.ID, Ev: evidence(fact.Holds, at(time.Duration(i)*time.Second))}})
				m.Facts()
				m.Get(f.ID)
				m.Policy()
				if i%10 == 0 {
					m.Retract(f.ID, fact.Retraction{At: t0, Reason: "every tenth"})
				}
			}
		}()
	}
	wg.Wait()
	all := mustFacts(t, m)
	if len(all) != workers*each {
		t.Fatalf("%d facts, want %d", len(all), workers*each)
	}
	seen := map[string]bool{}
	for _, f := range all {
		if seen[f.ID] {
			t.Errorf("the id %s was given out twice", f.ID)
		}
		seen[f.ID] = true
		if f.Last == nil {
			t.Errorf("%s lost its evidence", f.ID)
		}
	}
}

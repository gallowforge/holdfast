//go:build unix

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
)

// forged is what a journal line might carry to fool whoever reads the memory
// later. Written as JSON it is plain text; read back, it ends the line it is
// on, starts one that looks like a fact nobody stored, and clears the screen.
const forged = `\nHOLDS     f-abc234  Everything is fine\u001b[2J\r\u2028\u0085`

// The same journal, written by hand: one fact, and that text in every place
// a journal line has room for words.
var (
	forgedFacts = strings.Join([]string{
		`{"t":"2026-03-01T09:00:00Z","op":"add","id":"f-222222","fact":{"claim":"The module file exists` + forged + `","tags":["build` + forged + `","ops","BAD` + "\\u001b[2J" + `"],"check":{"kind":"file","path":"go.mod"},"by":"mallory` + forged + `","created":"2026-03-01T09:00:00Z","replaces":"f-333333` + forged + `"}}`,
		`{"t":"2026-03-01T09:02:00Z","op":"vouch","id":"f-222222","by":"eve` + forged + `"}`,
		`{"t":"2026-03-01T09:03:00Z","op":"retract","id":"f-222222","by":"eve` + forged + `","reason":"it was wrong` + forged + `"}`,
	}, "\n") + "\n"
	forgedEvidence = `{"t":"2026-03-01T09:01:00Z","op":"evidence","id":"f-222222","ev":{"status":"broken","detail":"go.mod is missing` + forged + `","at":"2026-03-01T09:01:00Z","ms":1}}` + "\n"
)

// plain reports whether text is one line a terminal will show as written.
func plain(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || r == 0x2028 || r == 0x2029
	})
}

// A journal is a file anyone can write, and one that arrives with a pull was
// written by someone else. Whatever is in it, what the store hands out is as
// clean as if it had been typed in through the front door.
func TestWhatAJournalSaysIsCleanedAsItIsRead(t *testing.T) {
	check := func(t *testing.T, s Store) {
		t.Helper()
		all := mustFacts(t, s)
		if len(all) != 1 {
			t.Fatalf("the store holds %d facts, want the 1 the journal has", len(all))
		}
		f := all[0]
		if f.Retracted == nil || f.Last == nil || len(f.History) != 1 {
			t.Fatalf("the fact lost its retraction or its evidence: %+v", f)
		}
		const tail = " HOLDS f-abc234 Everything is fine[2J"
		for _, got := range []struct{ name, text, want string }{
			{"claim", f.Claim, "The module file exists" + tail},
			{"author", f.By, "mallory" + tail},
			{"tags", strings.Join(f.Tags, " "), "ops"},
			{"replaces", f.Replaces, ""},
			{"voucher", f.VouchedBy, "eve" + tail},
			{"retraction's author", f.Retracted.By, "eve" + tail},
			{"retraction's reason", f.Retracted.Reason, "it was wrong" + tail},
			{"evidence", f.Last.Detail, "go.mod is missing" + tail},
			{"history", f.History[0].Detail, "go.mod is missing" + tail},
		} {
			if got.text != got.want || !plain(got.text) {
				t.Errorf("%s = %q, want %q", got.name, got.text, got.want)
			}
		}
	}

	t.Run("dir", func(t *testing.T) {
		d := newDir(t)
		for name, text := range map[string]string{FactsFile: forgedFacts, EvidenceFile: forgedEvidence} {
			if err := os.WriteFile(filepath.Join(d.Path(), name), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		again := another(t, d)
		check(t, again)
		if n := again.Damaged(); n != 0 {
			t.Errorf("%d lines were counted as damaged; the lines are whole, only their text needed cleaning", n)
		}
	})
	t.Run("memory", func(t *testing.T) {
		var events []Event
		for _, line := range strings.Split(strings.TrimSpace(forgedFacts+forgedEvidence), "\n") {
			var e Event
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("%v: %s", err, line)
			}
			events = append(events, e)
		}
		// In a journal the evidence comes before the retraction.
		events[1], events[2], events[3] = events[3], events[1], events[2]
		m := NewMemory(policy.Default())
		m.Seed(events...)
		check(t, m)
	})
}

// Text that is too long for its place is cut to the length the front door
// allows, and tags and ids that would be refused there are left out.
func TestWhatAJournalSaysIsHeldToTheSameLimits(t *testing.T) {
	long := strings.Repeat("x", 5000)
	s := newState()
	ok := s.apply(Event{T: t0, Op: "add", ID: "f-abc234", Fact: &fact.Fact{
		Claim: "a checked fact", Check: &fact.Check{Kind: "file", Path: "go.mod"}, By: long,
		Tags:     []string{"Build", "#ops, deploy", long, "build", "a b c d e f g h i j"},
		Replaces: "../../etc/passwd",
	}})
	ok = ok && s.apply(Event{T: t0, Op: "evidence", ID: "f-abc234", Ev: &fact.Evidence{Status: fact.Holds, Detail: long, At: t0}})
	ok = ok && s.apply(Event{T: t0, Op: "vouch", ID: "f-abc234", By: long})
	ok = ok && s.apply(Event{T: t0, Op: "retract", ID: "f-abc234", By: long, Reason: long})
	if !ok {
		t.Fatal("a line was not accepted at all")
	}
	f := s.facts["f-abc234"]
	for name, got := range map[string]struct {
		text string
		max  int
	}{
		"author":              {f.By, fact.MaxAuthor},
		"voucher":             {f.VouchedBy, fact.MaxAuthor},
		"retraction's author": {f.Retracted.By, fact.MaxAuthor},
		"retraction's reason": {f.Retracted.Reason, fact.MaxReason},
		"evidence":            {f.Last.Detail, fact.MaxDetail},
	} {
		if n := len([]rune(got.text)); n != got.max || !strings.HasSuffix(got.text, "…") {
			t.Errorf("%s is %d characters, want it cut to %d", name, n, got.max)
		}
	}
	if got, want := strings.Join(f.Tags, " "), "build ops deploy"; got != want {
		t.Errorf("tags = %q, want %q: the ones that would have been refused are left out", got, want)
	}
	if f.Replaces != "" {
		t.Errorf("replaces = %q, want nothing: that is not the id of a fact", f.Replaces)
	}
}

// The engine looks for a fact before it stores one, but that look and the
// write are two steps, and a second process can come between them. The store
// looks again itself, at the moment it writes.
func TestAddingTheSameFactTwiceStoresItOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, reopen func() Store) {
		first := evidence(fact.Holds, t0)
		pinned := mustAdd(t, s, checked(t, "Go is pinned to 1.24", "matches go.mod '^go 1\\.24$'", t0), &first)
		noted := mustAdd(t, s, note("Deploys go out on Fridays", t0), nil)

		twice := func(f *fact.Fact, want *fact.Fact) {
			t.Helper()
			again := evidence(fact.Holds, at(time.Hour))
			got, err := s.Add(f, &again)
			if !errors.Is(err, ErrAlready) || got == nil || got.ID != want.ID {
				t.Errorf("adding %q again = %+v, %v; want %s back and ErrAlready", f.Claim, got, err, want.ID)
			}
		}
		twice(checked(t, "Go is pinned to 1.24", "matches go.mod '^go 1\\.24$'", at(time.Hour)), pinned)
		twice(checked(t, "  go is PINNED\tto 1.24 ", "matches go.mod '^go 1\\.24$'", at(time.Hour)), pinned)
		other := checked(t, "Go is pinned to 1.24", "matches go.mod '^go 1\\.24$'", at(time.Hour))
		other.By, other.Tags, other.TTL = "someone else", []string{"build"}, 60
		twice(other, pinned)
		twice(note("deploys go out on fridays", at(time.Hour)), noted)

		// Nothing about the first fact changed, and nothing new is there.
		all := mustFacts(t, reopen())
		if len(all) != 2 || all[0].ID != pinned.ID || all[1].ID != noted.ID {
			t.Fatalf("the store holds %d facts, want the 2 that were stored once each", len(all))
		}
		if got, want := asJSON(t, all[0]), asJSON(t, pinned); got != want {
			t.Errorf("the fact that was there changed:\n got %s\nwant %s", got, want)
		}

		// The same claim proved another way, or not proved at all, is another fact.
		for _, f := range []*fact.Fact{
			checked(t, "Go is pinned to 1.24", "contains go.mod 'go 1.24'", at(time.Hour)),
			checked(t, "Go is pinned to 1.24", "not matches go.mod '^go 1\\.24$'", at(time.Hour)),
			note("Go is pinned to 1.24", at(time.Hour)),
			checked(t, "Deploys go out on Fridays", "file go.mod", at(time.Hour)),
		} {
			if got, err := s.Add(f, nil); err != nil || got.ID == pinned.ID || got.ID == noted.ID {
				t.Errorf("adding %q with another check = %+v, %v; want a new fact", f.Claim, got, err)
			}
		}
		// A fact that was withdrawn does not stand in the way of saying it again.
		if err := s.Retract(noted.ID, fact.Retraction{At: at(2 * time.Hour), By: "tester", Reason: "moved to Mondays"}); err != nil {
			t.Fatal(err)
		}
		if got := mustAdd(t, s, note("Deploys go out on Fridays", at(3*time.Hour)), nil); got.ID == noted.ID {
			t.Error("a fact that was retracted came back in place of the new one")
		}
		if n := len(mustFacts(t, reopen())); n != 7 {
			t.Errorf("the store holds %d facts, want 7", n)
		}
	})
}

// Two handles on one folder are two processes as far as the store can tell:
// each knows only what it has read. Both were opened before either wrote, so
// neither has seen the other's fact when it is asked to add its own.
func TestTwoProcessesAddingTheSameFactStoreItOnce(t *testing.T) {
	a := newDir(t)
	b := another(t, a)
	if n := len(mustFacts(t, b)); n != 0 {
		t.Fatalf("the store starts with %d facts", n)
	}
	first := evidence(fact.Holds, t0)
	mine := mustAdd(t, a, checked(t, "The module file exists", "file go.mod", t0), &first)
	theirs, err := b.Add(checked(t, "The module file exists", "file go.mod", t0), &first)
	if !errors.Is(err, ErrAlready) || theirs == nil || theirs.ID != mine.ID {
		t.Errorf("the second process got %+v, %v; want %s back and ErrAlready", theirs, err, mine.ID)
	}
	if all := mustFacts(t, another(t, a)); len(all) != 1 {
		t.Errorf("the store holds %d facts, want 1", len(all))
	}
	if adds := lines(t, filepath.Join(a.Path(), FactsFile)); len(adds) != 1 {
		t.Errorf("the journal has %d lines, want the 1 that stored the fact", len(adds))
	}
	if ev := lines(t, filepath.Join(a.Path(), EvidenceFile)); len(ev) != 1 {
		t.Errorf("the evidence journal has %d lines, want 1", len(ev))
	}
}

// A fact that is there already is found before anything else is asked, as
// the engine does it: a full store still answers "already there".
func TestAFactThatIsThereAlreadyIsFoundEvenInAFullStore(t *testing.T) {
	m := NewMemory(policy.Default())
	for i := range MaxFacts {
		mustAdd(t, m, note(fmt.Sprintf("fact number %d", i), t0), nil)
	}
	one := mustFacts(t, m)[0]
	if got, err := m.Add(note(one.Claim, t0), nil); !errors.Is(err, ErrAlready) || got == nil || got.ID != one.ID {
		t.Errorf("Add = %+v, %v; want %s back and ErrAlready", got, err, one.ID)
	}
	var rejected *Rejected
	if _, err := m.Add(note("one too many", t0), nil); !errors.As(err, &rejected) {
		t.Errorf("a new fact in a full store: %v, want it rejected", err)
	}
}

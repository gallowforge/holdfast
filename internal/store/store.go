// Package store keeps facts.
//
// A fact is shared and its proof is local. What was claimed, by whom, and what
// was later retracted lives in facts.jsonl, which a team can commit. Evidence
// lives in evidence.jsonl, which stays on the machine that gathered it: every
// machine proves facts for itself.
//
// Both files are journals: one JSON event per line, only ever appended to.
package store

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
)

// Refresh is how often unchanged evidence is written down again. A check that
// keeps passing is journaled at most this often, so the journal records "still
// held at" without growing on every recall.
const Refresh = 5 * time.Minute

// MaxFacts caps how many live facts one store holds.
const MaxFacts = 2000

// ErrNotFound is returned for an id no fact has.
var ErrNotFound = errors.New("there is no fact with that id")

// ErrAlready is returned by Add, together with the fact that was found, when
// a live fact already makes the same claim with the same check. Nothing new
// was stored.
var ErrAlready = errors.New("a fact that says the same thing with the same check is already stored")

// Rejected is a change the store will not make, said in words meant for
// whoever asked for it.
type Rejected struct{ Why string }

func (r *Rejected) Error() string { return r.Why }

// Event is one line of a journal.
type Event struct {
	T      time.Time      `json:"t"`
	Op     string         `json:"op"` // add, retract, vouch (facts.jsonl); evidence (evidence.jsonl)
	ID     string         `json:"id"`
	Fact   *fact.Fact     `json:"fact,omitempty"`
	Ev     *fact.Evidence `json:"ev,omitempty"`
	By     string         `json:"by,omitempty"`
	Reason string         `json:"reason,omitempty"`
}

// Proof pairs a fact with fresh evidence about it.
type Proof struct {
	ID string
	Ev fact.Evidence
}

// Store is where the engine keeps facts. Every method returns copies; nothing
// a caller holds changes behind its back.
type Store interface {
	// Facts returns every fact, retracted ones included, oldest first.
	Facts() ([]*fact.Fact, error)
	Get(id string) (*fact.Fact, error)
	// Add stores a new fact, giving it an id, with the evidence from its first
	// check if it has one. A fact that is there already is not stored twice:
	// Add returns the one that is there, and ErrAlready.
	Add(f *fact.Fact, ev *fact.Evidence) (*fact.Fact, error)
	// Record notes fresh evidence. Not every call reaches the journal: see Refresh.
	Record(batch []Proof) error
	Retract(id string, r fact.Retraction) error
	Vouch(id, by string, at time.Time) error
	// Policy returns the rules checks run under. The note is not empty when the
	// policy on disk is not the one in force, and says what a person can do.
	Policy() (p *policy.Policy, note string)
}

// state is the facts as the journals describe them.
type state struct {
	facts   map[string]*fact.Fact
	order   []string
	written map[string]time.Time // when each fact's evidence last reached the journal
	live    int
	damaged int
}

func newState() *state {
	return &state{facts: map[string]*fact.Fact{}, written: map[string]time.Time{}}
}

// apply plays one event. It reports false for an event that makes no sense
// here, which a journal reader counts as damage or simply skips.
func (s *state) apply(e Event) bool {
	switch e.Op {
	case "add":
		if e.Fact == nil || !fact.ValidID(e.ID) || s.facts[e.ID] != nil {
			return false
		}
		f := e.Fact.Clone()
		claim, err := fact.CleanClaim(f.Claim)
		if err != nil || (f.Check != nil && f.Check.Validate() != nil) {
			return false
		}
		// Only what was claimed comes from the journal line; everything about
		// proof and standing is worked out from the events that follow. A
		// journal is a file anyone can write, and one that arrives with a
		// pull was written by someone else, so its text is cleaned exactly as
		// if it had just been typed in.
		if !fact.ValidID(f.Replaces) {
			f.Replaces = ""
		}
		*f = fact.Fact{
			ID: e.ID, Claim: claim, Tags: cleanTags(f.Tags), Check: f.Check, By: fact.CleanLine(f.By, fact.MaxAuthor),
			Created: f.Created, TTL: f.TTL, Replaces: f.Replaces,
		}
		if f.Created.IsZero() {
			f.Created = e.T
		}
		s.facts[f.ID] = f
		s.order = append(s.order, f.ID)
		s.live++
		if old := s.facts[f.Replaces]; old != nil && old.ReplacedBy == "" && old.ID != f.ID {
			if old.Live() {
				s.live--
			}
			old.ReplacedBy = f.ID
		}
		return true
	case "evidence":
		f := s.facts[e.ID]
		if f == nil || e.Ev == nil || f.Check == nil {
			return false
		}
		switch e.Ev.Status {
		case fact.Holds, fact.Broken, fact.Blocked:
		default:
			return false
		}
		ev := *e.Ev
		ev.Detail = fact.CleanLine(ev.Detail, fact.MaxDetail)
		ev.Fix = fact.WholeLine(ev.Fix, fact.MaxDetail)
		s.prove(f, ev)
		s.written[f.ID] = ev.At
		return true
	case "retract":
		f := s.facts[e.ID]
		if f == nil {
			return false
		}
		if f.Retracted == nil {
			if f.Live() {
				s.live--
			}
			f.Retracted = &fact.Retraction{
				At: e.T, By: fact.CleanLine(e.By, fact.MaxAuthor), Reason: fact.CleanLine(e.Reason, fact.MaxReason),
			}
		}
		return true
	case "vouch":
		f := s.facts[e.ID]
		if f == nil {
			return false
		}
		// At the same moment the later line wins: the clock is kept to the
		// millisecond, and the journal is in the order things were said.
		if !e.T.Before(f.Vouched) {
			f.Vouched, f.VouchedBy = e.T, fact.CleanLine(e.By, fact.MaxAuthor)
		}
		return true
	}
	return false
}

// cleanTags keeps the tags of a journal line that would have been accepted
// when the fact was stored, and drops the rest.
func cleanTags(in []string) []string {
	var out []string
	for _, t := range in {
		if with, err := fact.CleanTags(append(out[:len(out):len(out)], t)); err == nil {
			out = with
		}
	}
	return out
}

// prove folds one piece of evidence into a fact.
func (s *state) prove(f *fact.Fact, ev fact.Evidence) {
	if f.Last == nil || f.Last.Status != ev.Status {
		f.Since = ev.At
		f.History = append(f.History, ev)
		if n := len(f.History) - fact.MaxHistory; n > 0 {
			f.History = append([]fact.Evidence(nil), f.History[n:]...)
		}
	}
	if ev.Status == fact.Holds {
		f.Held = ev.At
	}
	f.Last = &ev
}

// worthWriting says whether evidence should reach the journal: when the
// fact's state changed, or when what the journal has is getting old.
func (s *state) worthWriting(f *fact.Fact, ev fact.Evidence) bool {
	return f.Last == nil || f.Last.Status != ev.Status || ev.At.Sub(s.written[f.ID]) >= Refresh
}

func (s *state) all() []*fact.Fact {
	out := make([]*fact.Fact, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.facts[id].Clone())
	}
	return out
}

// Same reports whether two facts are one fact said twice: the same claim,
// whatever its capitals, proved by the same check.
func Same(a, b *fact.Fact) bool {
	if !strings.EqualFold(a.Claim, b.Claim) {
		return false
	}
	if a.Check == nil || b.Check == nil {
		return a.Check == b.Check
	}
	return a.Check.Short() == b.Check.Short()
}

// twin finds the live fact that already says what f says. The engine looks
// for one before it runs a check, but two processes can both look, both find
// nothing and both add. Only a look made here, under the lock that guards the
// write, is sure to see what the other one stored.
func (s *state) twin(f *fact.Fact) *fact.Fact {
	said := *f
	if claim, err := fact.CleanClaim(f.Claim); err == nil {
		said.Claim = claim // as it would be stored
	}
	for _, id := range s.order {
		if old := s.facts[id]; old.Live() && Same(old, &said) {
			return old
		}
	}
	return nil
}

// admit decides whether a new fact may be added.
func (s *state) admit(f *fact.Fact) error {
	if s.live >= MaxFacts {
		return &Rejected{fmt.Sprintf("this store already holds %d facts; retract some before adding more", s.live)}
	}
	if f.Replaces == "" {
		return nil
	}
	old := s.facts[f.Replaces]
	switch {
	case old == nil:
		return fmt.Errorf("%s cannot be replaced: %w", f.Replaces, ErrNotFound)
	case old.Retracted != nil:
		return &Rejected{old.ID + " was already retracted"}
	case old.ReplacedBy != "":
		return &Rejected{old.ID + " was already replaced by " + old.ReplacedBy}
	}
	return nil
}

// newID picks an id no fact has yet.
func (s *state) newID(random io.Reader) (string, error) {
	if random == nil {
		random = rand.Reader
	}
	buf := make([]byte, 16)
	for range 64 {
		if _, err := io.ReadFull(random, buf); err != nil {
			return "", err
		}
		if id := fact.NewID(buf); s.facts[id] == nil {
			return id, nil
		}
	}
	return "", errors.New("could not find a free id")
}

// Memory is a store that lives in memory: for the live demo and for tests.
type Memory struct {
	mu    sync.Mutex
	st    *state
	rules *policy.Policy
	// Rand supplies randomness for ids; nil means the system's.
	Rand io.Reader
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty store with the given rules.
func NewMemory(rules *policy.Policy) *Memory {
	return &Memory{st: newState(), rules: rules}
}

// Seed plays events into the store as if they had been read from a journal,
// so a demo can start with a past.
func (m *Memory) Seed(events ...Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range events {
		if !m.st.apply(e) {
			panic(fmt.Sprintf("store: bad seed event %s %s", e.Op, e.ID))
		}
	}
}

// SetPolicy replaces the rules.
func (m *Memory) SetPolicy(p *policy.Policy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules = p
}

func (m *Memory) Policy() (*policy.Policy, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rules, ""
}

func (m *Memory) Facts() ([]*fact.Fact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.all(), nil
}

func (m *Memory) Get(id string) (*fact.Fact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.st.facts[id]
	if f == nil {
		return nil, ErrNotFound
	}
	return f.Clone(), nil
}

func (m *Memory) Add(f *fact.Fact, ev *fact.Evidence) (*fact.Fact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.st.twin(f); old != nil {
		return old.Clone(), ErrAlready
	}
	if err := m.st.admit(f); err != nil {
		return nil, err
	}
	id, err := m.st.newID(m.Rand)
	if err != nil {
		return nil, err
	}
	m.st.apply(Event{T: f.Created, Op: "add", ID: id, Fact: f})
	if ev != nil {
		m.st.apply(Event{T: ev.At, Op: "evidence", ID: id, Ev: ev})
	}
	return m.st.facts[id].Clone(), nil
}

func (m *Memory) Record(batch []Proof) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range batch {
		if f := m.st.facts[p.ID]; f != nil && f.Check != nil {
			m.st.prove(f, p.Ev)
		}
	}
	return nil
}

func (m *Memory) Retract(id string, r fact.Retraction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.st.facts[id] == nil {
		return ErrNotFound
	}
	m.st.apply(Event{T: r.At, Op: "retract", ID: id, By: r.By, Reason: r.Reason})
	return nil
}

func (m *Memory) Vouch(id, by string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.st.facts[id] == nil {
		return ErrNotFound
	}
	m.st.apply(Event{T: at, Op: "vouch", ID: id, By: by})
	return nil
}

// Package engine is Holdfast itself: remembering facts, proving them, and
// answering with what still holds. The command line, the MCP server, the web
// interface and the live demo are all thin layers over this package.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/proof"
	"github.com/gallowforge/holdfast/internal/store"
	"github.com/gallowforge/holdfast/internal/world"
)

const (
	// DefaultLimit is how many facts a recall returns unless asked otherwise.
	DefaultLimit = 8
	// MaxLimit is the most facts one recall returns.
	MaxLimit = 50
	// Workers is how many checks run at once.
	Workers = 8
	// RecallBudget is how long a recall may spend re-proving what it found.
	// A fact whose check does not finish in time is returned as it was last
	// known, and marked as not re-checked.
	RecallBudget = 12 * time.Second
	// VerifyBudget is the same limit for an explicit verify.
	VerifyBudget = 90 * time.Second
	// Recent is how long an answer from the network or from a command counts
	// as current: a recall does not repeat a check like that if it ran this
	// recently. A check that only reads files costs nothing, so it is run
	// again every time; so is one the policy turned away, in case a person
	// has allowed it since.
	Recent = 30 * time.Second
	// MaxQuery caps the length of a recall query.
	MaxQuery = 300
)

// Refused is returned when Holdfast will not do what was asked. Its text is
// written for whoever asked, agent or person, and says what to do instead.
type Refused struct{ Why string }

func (r *Refused) Error() string { return r.Why }

func refuse(format string, a ...any) error { return &Refused{Why: fmt.Sprintf(format, a...)} }

// Engine ties a store to the machine its checks run against.
type Engine struct {
	Store store.Store
	World world.World
}

// View is a fact as shown to the outside: the same shape in the web
// interface, the JSON output and the structured results sent to agents.
type View struct {
	ID      string      `json:"id"`
	Claim   string      `json:"claim"`
	Tags    []string    `json:"tags,omitempty"`
	Check   string      `json:"check,omitempty"`
	Kind    string      `json:"kind,omitempty"`
	By      string      `json:"by,omitempty"`
	Created time.Time   `json:"created"`
	Status  fact.Status `json:"status"`
	// Detail is the evidence line from the last check.
	Detail string `json:"detail,omitempty"`
	// Fix is what a person can run to allow a blocked check, when there is
	// such a line. Detail ends with the same words; this is the copy to use.
	Fix       string     `json:"fix,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	Ms        int64      `json:"ms,omitempty"`
	// Since is when the fact entered its current state; Held is when its
	// check last passed.
	Since *time.Time `json:"since,omitempty"`
	Held  *time.Time `json:"held,omitempty"`
	// NotedAt and StaleAt describe a fact without a check: when someone last
	// stated it, and when it stops being believed.
	NotedAt    *time.Time       `json:"noted_at,omitempty"`
	StaleAt    *time.Time       `json:"stale_at,omitempty"`
	VouchedBy  string           `json:"vouched_by,omitempty"`
	Replaces   string           `json:"replaces,omitempty"`
	ReplacedBy string           `json:"replaced_by,omitempty"`
	Retracted  *fact.Retraction `json:"retracted,omitempty"`
	History    []fact.Evidence  `json:"history,omitempty"`
	// Fresh is true when the check ran during this very call. Late is true
	// when it should have but did not finish in time.
	Fresh bool `json:"fresh,omitempty"`
	Late  bool `json:"late,omitempty"`
}

// TimedOut reports whether the fact is shown as holding only because that is
// how it was last seen: its check should have run in this call and did not
// finish. That is not proof, so the plain-text forms label it TIMED OUT.
func (v View) TimedOut() bool { return v.Late && v.Status == fact.Holds }

// timedOutWord is the status the JSON form gives a timed-out fact.
const timedOutWord = "timed_out"

// MarshalJSON writes a timed-out fact with the status "timed_out", so that no
// reader of the JSON finds "holds" on a fact that was not proved in this call.
func (v View) MarshalJSON() ([]byte, error) {
	type plain View
	p := plain(v)
	if v.TimedOut() {
		p.Status = timedOutWord
	}
	return json.Marshal(p)
}

// UnmarshalJSON reads what MarshalJSON writes.
func (v *View) UnmarshalJSON(data []byte) error {
	type plain View
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if p.Status == timedOutWord {
		p.Status, p.Late = fact.Holds, true
	}
	*v = View(p)
	return nil
}

// Counts sums up a store.
type Counts struct {
	Total    int `json:"total"`
	Holds    int `json:"holds"`
	Broken   int `json:"broken"`
	Blocked  int `json:"blocked"`
	Unproven int `json:"unproven"`
	Stale    int `json:"stale"`
	// TimedOut is how many facts held when last checked but could not be
	// checked again in time. They are not proved, so they are not under Holds.
	TimedOut int `json:"timed_out,omitempty"`
	// Late is how many facts could not be checked again in time, whatever
	// they were before: the timed out ones, and any that did not hold anyway.
	Late int `json:"late,omitempty"`
}

func (c *Counts) add(s fact.Status) {
	c.Total++
	switch s {
	case fact.Holds:
		c.Holds++
	case fact.Broken:
		c.Broken++
	case fact.Blocked:
		c.Blocked++
	case fact.Stale:
		c.Stale++
	default:
		c.Unproven++
	}
}

func count(views []View) Counts {
	var c Counts
	for _, v := range views {
		if v.Late {
			c.Late++
		}
		if v.TimedOut() {
			c.Total++
			c.TimedOut++
			continue
		}
		c.add(v.Status)
	}
	return c
}

// Count sums up the facts given, as a list or a recall would.
func Count(views []View) Counts { return count(views) }

func (e *Engine) view(f *fact.Fact, now time.Time) View {
	v := View{
		ID: f.ID, Claim: f.Claim, Tags: f.Tags, By: f.By, Created: f.Created,
		Status: f.StatusAt(now), VouchedBy: f.VouchedBy,
		Replaces: f.Replaces, ReplacedBy: f.ReplacedBy, Retracted: f.Retracted,
		History: f.History,
	}
	at := func(t time.Time) *time.Time {
		if t.IsZero() {
			return nil
		}
		return &t
	}
	if f.Check == nil {
		noted := f.NotedAt()
		v.NotedAt = at(noted)
		v.StaleAt = at(noted.Add(f.TimeToLive()))
		return v
	}
	v.Check, v.Kind = f.Check.Short(), f.Check.Kind
	v.Since, v.Held = at(f.Since), at(f.Held)
	if f.Last != nil {
		v.Detail, v.Fix, v.Ms, v.CheckedAt = f.Last.Detail, f.Last.Fix, f.Last.Ms, at(f.Last.At)
	} else {
		v.Detail = "this check has not run on this machine yet"
	}
	return v
}

// byAttention orders views so what needs a person comes first: broken,
// timed out, blocked, stale, unproven, then what holds; newest change first
// within each.
func byAttention(views []View) {
	sort.SliceStable(views, func(i, j int) bool {
		a, b := views[i], views[j]
		if attention(a) != attention(b) {
			return attention(a) < attention(b)
		}
		return moment(a).After(moment(b))
	})
}

// attention ranks a view by how much it needs a person. A timed-out fact is
// not proved, so it comes straight after the broken ones, never with what holds.
func attention(v View) int {
	if v.TimedOut() {
		return 2*fact.Broken.Rank() + 1
	}
	return 2 * v.Status.Rank()
}

func moment(v View) time.Time {
	switch {
	case v.Since != nil:
		return *v.Since
	case v.NotedAt != nil:
		return *v.NotedAt
	}
	return v.Created
}

// Claim is what someone asks Holdfast to remember.
type Claim struct {
	Claim    string
	Check    string // in the check grammar; empty for an unproven note
	Tags     []string
	Replaces string // id of a fact this one supersedes
	TTL      string // for a fact without a check: how long to believe it
	By       string
}

// Remember stores a fact. A fact with a check is only stored if the check
// passes right now (or cannot be run yet because the policy does not allow
// it); a check that fails is refused, because that is not a fact.
//
// The boolean is true when the same fact was already there.
func (e *Engine) Remember(ctx context.Context, in Claim) (View, bool, error) {
	now := e.World.Now().UTC().Truncate(time.Millisecond)
	claim, err := fact.CleanClaim(in.Claim)
	if err != nil {
		return View{}, false, &Refused{Why: err.Error()}
	}
	tags, err := fact.CleanTags(in.Tags)
	if err != nil {
		return View{}, false, &Refused{Why: err.Error()}
	}
	f := &fact.Fact{
		Claim: claim, Tags: tags, Created: now,
		By:       fact.CleanLine(in.By, fact.MaxAuthor),
		Replaces: strings.TrimSpace(in.Replaces),
	}
	if f.Replaces != "" && !fact.ValidID(f.Replaces) {
		return View{}, false, refuse("%q is not a fact id; ids look like f-abc234", in.Replaces)
	}
	if strings.TrimSpace(in.TTL) != "" {
		d, err := fact.ParseDuration(in.TTL)
		if err != nil {
			return View{}, false, &Refused{Why: err.Error()}
		}
		f.TTL = fact.Seconds(d / time.Second)
	}

	text := strings.TrimSpace(in.Check)
	if len(text) > fact.MaxCheck {
		return View{}, false, refuse("the check is %d characters; the limit is %d", len(text), fact.MaxCheck)
	}
	var first *fact.Evidence
	if text != "" {
		c, err := fact.ParseCheck(text)
		if err != nil {
			return View{}, false, &Refused{Why: err.Error()}
		}
		c.Sum = "" // the fingerprint is always taken here, never supplied
		f.Check = c
	}

	all, err := e.Store.Facts()
	if err != nil {
		return View{}, false, err
	}
	for _, old := range all {
		if old.Live() && store.Same(old, f) {
			return e.view(old, now), true, nil
		}
	}

	if f.Check != nil {
		rules, _ := e.Store.Policy()
		err := proof.Seal(ctx, e.World, rules, f.Check)
		// A caller that stopped waiting cuts the check short. Nothing was
		// proved either way, so nothing is stored and nothing is refused.
		if ctx.Err() != nil {
			return View{}, false, ctx.Err()
		}
		if err != nil {
			return View{}, false, refuse("cannot store this: %s", err)
		}
		ev := proof.Run(ctx, e.World, rules, f.Check)
		if ctx.Err() != nil {
			return View{}, false, ctx.Err()
		}
		if ev.Status == fact.Broken {
			return View{}, false, refuse("the check fails right now, so this is not a fact yet: %s. "+
				"Correct the claim or the check; or, to record that something is not so, start the check with \"not\".", ev.Detail)
		}
		// A fact with a check takes another's place only once that check has
		// run: otherwise a fact that held is withdrawn for one nobody checked.
		if ev.Status == fact.Blocked && f.Replaces != "" {
			return View{}, false, refuse("the new fact's check was not run, so it cannot replace %s yet: %s", f.Replaces, ev.Detail)
		}
		first = &ev
	}

	stored, err := e.Store.Add(f, first)
	if errors.Is(err, store.ErrAlready) {
		// Someone else stored the same fact after the look above: another
		// process, or another agent on this server. It is reported exactly as
		// if it had been found then.
		return e.view(stored, now), true, nil
	}
	if err != nil {
		var rejected *store.Rejected
		if errors.Is(err, store.ErrNotFound) || errors.As(err, &rejected) {
			return View{}, false, &Refused{Why: err.Error()}
		}
		return View{}, false, err
	}
	v := e.view(stored, now)
	v.Fresh = first != nil
	return v, false, nil
}

// Recalled is the answer to a recall.
type Recalled struct {
	Facts []View `json:"facts"`
	// Matched is how many facts matched before the limit was applied.
	Matched int `json:"matched"`
	// Known is how many live facts the store holds.
	Known int `json:"known"`
	// Note is set when the policy on disk is not the one in force.
	Note string `json:"note,omitempty"`
}

// Recall finds the facts that bear on a question and proves each one again
// before returning it. An empty query returns everything, most urgent first.
func (e *Engine) Recall(ctx context.Context, query string, limit int) (Recalled, error) {
	now := e.World.Now()
	if len(query) > MaxQuery {
		query = query[:MaxQuery]
	}
	words := tokens(query)
	if limit <= 0 {
		limit = DefaultLimit
		if len(words) == 0 {
			limit = 20
		}
	}
	limit = min(limit, MaxLimit)

	all, err := e.Store.Facts()
	if err != nil {
		return Recalled{}, err
	}
	type hit struct {
		f     *fact.Fact
		score int
	}
	var hits []hit
	out := Recalled{}
	for _, f := range all {
		if !f.Live() {
			continue
		}
		out.Known++
		s := 1
		if len(words) > 0 {
			s = score(f, words)
		}
		if s > 0 {
			hits = append(hits, hit{f, s})
		}
	}
	out.Matched = len(hits)
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if ra, rb := a.f.StatusAt(now).Rank(), b.f.StatusAt(now).Rank(); ra != rb {
			return ra < rb
		}
		return a.f.Created.After(b.f.Created)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	picked := make([]*fact.Fact, len(hits))
	scores := map[string]int{}
	for i, h := range hits {
		picked[i] = h.f
		scores[h.f.ID] = h.score
	}
	views, err := e.reprove(ctx, picked, RecallBudget, Recent)
	if err != nil {
		return Recalled{}, err
	}
	sort.SliceStable(views, func(i, j int) bool {
		a, b := views[i], views[j]
		if scores[a.ID] != scores[b.ID] {
			return scores[a.ID] > scores[b.ID]
		}
		return attention(a) < attention(b)
	})
	out.Facts = views
	_, out.Note = e.Store.Policy()
	return out, nil
}

// Verified is the answer to a verify.
type Verified struct {
	Facts  []View `json:"facts"`
	Counts Counts `json:"counts"`
	Note   string `json:"note,omitempty"`
}

// Verify proves facts again now: the ones named, or every live fact that has
// a check.
func (e *Engine) Verify(ctx context.Context, ids []string) (Verified, error) {
	all, err := e.Store.Facts()
	if err != nil {
		return Verified{}, err
	}
	var picked []*fact.Fact
	if len(ids) == 0 {
		for _, f := range all {
			if f.Live() && f.Check != nil {
				picked = append(picked, f)
			}
		}
	} else {
		byID := map[string]*fact.Fact{}
		for _, f := range all {
			byID[f.ID] = f
		}
		seen := map[string]bool{}
		for _, id := range ids {
			id = strings.TrimSpace(id)
			f := byID[id]
			switch {
			case f == nil:
				return Verified{}, refuse("there is no fact with the id %q", id)
			case seen[id]:
			default:
				seen[id] = true
				picked = append(picked, f)
			}
		}
	}
	views, err := e.reprove(ctx, picked, VerifyBudget, 0)
	if err != nil {
		return Verified{}, err
	}
	byAttention(views)
	out := Verified{Facts: views, Counts: count(views)}
	_, out.Note = e.Store.Policy()
	return out, nil
}

// reprove runs the checks of the given facts, writes down what it saw, and
// returns the facts as they now stand, in the order given. Evidence younger
// than recent is trusted as it is.
func (e *Engine) reprove(ctx context.Context, facts []*fact.Fact, budget, recent time.Duration) ([]View, error) {
	now := e.World.Now()
	var due []*fact.Fact
	for _, f := range facts {
		if f.Check == nil || !f.Live() {
			continue
		}
		if f.Last != nil && recent > 0 && !f.Check.Local() && f.Last.Status != fact.Blocked &&
			now.Sub(f.Last.At) < recent && !now.Before(f.Last.At) {
			continue
		}
		due = append(due, f)
	}

	got := map[string]fact.Evidence{}
	if len(due) > 0 {
		rules, _ := e.Store.Policy()
		ctx, cancel := context.WithTimeout(ctx, budget)
		results := make(chan store.Proof, len(due))
		slots := make(chan struct{}, Workers)
		for _, f := range due {
			go func() {
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-slots }()
				ev := proof.Run(ctx, e.World, rules, f.Check)
				// A check cut short by the budget says nothing about the
				// fact, so its outcome is thrown away.
				if ctx.Err() == nil {
					results <- store.Proof{ID: f.ID, Ev: ev}
				}
			}()
		}
	collect:
		for range due {
			select {
			case r := <-results:
				got[r.ID] = r.Ev
			case <-ctx.Done():
				break collect
			}
		}
		cancel()
		batch := make([]store.Proof, 0, len(got))
		for _, f := range due {
			if ev, ok := got[f.ID]; ok {
				batch = append(batch, store.Proof{ID: f.ID, Ev: ev})
			}
		}
		if err := e.Store.Record(batch); err != nil {
			return nil, err
		}
	}

	current := map[string]*fact.Fact{}
	if len(got) > 0 {
		all, err := e.Store.Facts()
		if err != nil {
			return nil, err
		}
		for _, f := range all {
			current[f.ID] = f
		}
	}
	wanted := map[string]bool{}
	for _, f := range due {
		wanted[f.ID] = true
	}
	now = e.World.Now()
	views := make([]View, 0, len(facts))
	for _, f := range facts {
		if c := current[f.ID]; c != nil {
			f = c
		}
		v := e.view(f, now)
		if ev, ok := got[f.ID]; ok {
			// Show exactly what this call saw, even if another process has
			// written newer evidence since.
			v.Fresh, v.Status, v.Detail, v.Fix, v.Ms = true, ev.Status, ev.Detail, ev.Fix, ev.Ms
			at := ev.At
			v.CheckedAt = &at
		} else if wanted[f.ID] {
			v.Late = true
		}
		views = append(views, v)
	}
	return views, nil
}

// Retract withdraws a fact. It stays in the journal, with who withdrew it
// and why, and is never returned by a recall again.
func (e *Engine) Retract(id, reason, by string) (View, error) {
	id = strings.TrimSpace(id)
	f, err := e.Store.Get(id)
	if errors.Is(err, store.ErrNotFound) {
		return View{}, refuse("there is no fact with the id %q", id)
	}
	if err != nil {
		return View{}, err
	}
	if f.Retracted != nil {
		return View{}, refuse("%s was already retracted", id)
	}
	reason = fact.CleanLine(reason, fact.MaxReason)
	if reason == "" {
		return View{}, refuse("say why %s is being retracted, in a few words: the reason is kept with it", id)
	}
	now := e.World.Now().UTC().Truncate(time.Millisecond)
	r := fact.Retraction{At: now, By: fact.CleanLine(by, fact.MaxAuthor), Reason: reason}
	if err := e.Store.Retract(id, r); err != nil {
		return View{}, err
	}
	f, err = e.Store.Get(id)
	if err != nil {
		return View{}, err
	}
	return e.view(f, now), nil
}

// Vouch records that a person confirmed a fact that has no check, which
// restarts its time to live. Only a person can vouch: agents cannot reach it.
func (e *Engine) Vouch(id, by string) (View, error) {
	id = strings.TrimSpace(id)
	f, err := e.Store.Get(id)
	if errors.Is(err, store.ErrNotFound) {
		return View{}, refuse("there is no fact with the id %q", id)
	}
	if err != nil {
		return View{}, err
	}
	switch {
	case !f.Live():
		return View{}, refuse("%s has been retracted or replaced", id)
	case f.Check != nil:
		return View{}, refuse("%s has a check, so it proves itself; there is nothing to vouch for", id)
	}
	now := e.World.Now().UTC().Truncate(time.Millisecond)
	if err := e.Store.Vouch(id, fact.CleanLine(by, fact.MaxAuthor), now); err != nil {
		return View{}, err
	}
	f, err = e.Store.Get(id)
	if err != nil {
		return View{}, err
	}
	return e.view(f, now), nil
}

// Get returns one fact as last known, without running its check.
func (e *Engine) Get(id string) (View, error) {
	id = strings.TrimSpace(id)
	f, err := e.Store.Get(id)
	if errors.Is(err, store.ErrNotFound) {
		return View{}, refuse("there is no fact with the id %q", id)
	}
	if err != nil {
		return View{}, err
	}
	return e.view(f, e.World.Now()), nil
}

// Listed is every fact as last known.
type Listed struct {
	Facts  []View `json:"facts"`
	Counts Counts `json:"counts"`
	// Gone holds retracted and replaced facts, newest first; only filled
	// when asked for.
	Gone []View `json:"gone,omitempty"`
	Note string `json:"note,omitempty"`
}

// Mark notes on a list the facts whose checks did not finish in the call that
// just tried to prove them, so the list cannot show them as holding. It
// returns how many there were.
func (l *Listed) Mark(proved []View) int {
	late := map[string]bool{}
	for _, v := range proved {
		if v.Late {
			late[v.ID] = true
		}
	}
	for i := range l.Facts {
		l.Facts[i].Late = late[l.Facts[i].ID]
	}
	byAttention(l.Facts)
	l.Counts = count(l.Facts)
	return len(late)
}

// List returns every live fact as last known, without running any check,
// most urgent first.
func (e *Engine) List(gone bool) (Listed, error) {
	all, err := e.Store.Facts()
	if err != nil {
		return Listed{}, err
	}
	now := e.World.Now()
	out := Listed{Facts: []View{}}
	for _, f := range all {
		switch {
		case f.Live():
			out.Facts = append(out.Facts, e.view(f, now))
		case gone:
			out.Gone = append(out.Gone, e.view(f, now))
		}
	}
	byAttention(out.Facts)
	for i, j := 0, len(out.Gone)-1; i < j; i, j = i+1, j-1 {
		out.Gone[i], out.Gone[j] = out.Gone[j], out.Gone[i]
	}
	out.Counts = count(out.Facts)
	_, out.Note = e.Store.Policy()
	return out, nil
}

// tokens cuts text into the lower-case words worth matching on.
func tokens(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if seen[w] || filler[w] {
			continue
		}
		if len([]rune(w)) < 2 && !unicode.IsDigit([]rune(w)[0]) {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// filler lists words too common to say anything about which fact is meant.
var filler = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields("a an the is are was were be been it its this that these those of on in at to for from by with " +
		"and or not do does did what which who how where when why can should would will we our you your i my me " +
		"about any there here has have had as if so than then") {
		m[w] = true
	}
	return m
}()

// score says how well a fact answers a query: a word of the claim or a tag
// counts most, a near match less, a mention in the check least.
func score(f *fact.Fact, query []string) int {
	claim := tokens(f.Claim)
	var tags []string
	for _, t := range f.Tags {
		tags = append(tags, tokens(t)...)
	}
	check := ""
	if f.Check != nil {
		check = strings.ToLower(f.Check.Short())
	}
	total := 0
	for _, q := range query {
		best := 0
		for _, w := range claim {
			switch {
			case w == q:
				best = max(best, 3)
			case near(q, w):
				best = max(best, 2)
			}
		}
		for _, w := range tags {
			if w == q {
				best = max(best, 3)
			}
		}
		if best == 0 && check != "" && strings.Contains(check, q) {
			best = 1
		}
		total += best
	}
	return total
}

// near reports whether two words are the same word in different clothes:
// "port" and "ports", "listen" and "listening".
func near(a, b string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= 4 && strings.HasPrefix(b, a) || len(a) >= 3 && len(b)-len(a) <= 2 && strings.HasPrefix(b, a)
}

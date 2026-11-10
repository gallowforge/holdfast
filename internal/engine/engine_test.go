package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/sim"
	"github.com/gallowforge/holdfast/internal/store"
	"github.com/gallowforge/holdfast/internal/world"
)

// start is when every test begins: a fixed moment, so nothing depends on the
// day the tests run.
var start = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

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

const goMod = "module example.com/atlas\n\ngo 1.24\n"

// bench is an engine on a pretend machine: a few files, one open port and
// two commands, with a policy that allows the project folder, this machine's
// own network and exactly those commands.
type bench struct {
	*Engine
	t     *testing.T
	clock *clock
	world *sim.World
	store *store.Memory
}

func rules(t testing.TB, text string) *policy.Policy {
	t.Helper()
	p, err := policy.Parse(text)
	if err != nil {
		t.Fatalf("the test's own policy does not parse: %v", err)
	}
	return p
}

const benchPolicy = "files .\nnet loopback\ncmd git status --short\ncmd node --version\ncmd make test\n"

func newBench(t *testing.T) *bench {
	t.Helper()
	c := &clock{t: start}
	w := sim.New(c.now)
	w.Write("go.mod", goMod, start.Add(-time.Hour))
	w.Write("go.sum", "example.com/dep v1.0.0 h1:abc=\n", start.Add(-time.Hour))
	w.Write("config/app.yaml", "port: 8080\nmode: production\n", start.Add(-time.Hour))
	w.Ports["localhost:8080"] = true
	w.Cmds["git status --short"] = sim.Result{}
	w.Cmds["node --version"] = sim.Result{Out: "v22.3.0\n"}
	s := store.NewMemory(rules(t, benchPolicy))
	return &bench{Engine: &Engine{Store: s, World: w}, t: t, clock: c, world: w, store: s}
}

// remember stores a fact that must be accepted.
func (b *bench) remember(claim, check string, tags ...string) View {
	b.t.Helper()
	v, already, err := b.Remember(context.Background(), Claim{Claim: claim, Check: check, Tags: tags, By: "claude"})
	if err != nil {
		b.t.Fatalf("Remember(%q, %q): %v", claim, check, err)
	}
	if already {
		b.t.Fatalf("Remember(%q, %q) says it was already there", claim, check)
	}
	return v
}

func (b *bench) change(fn func(w *sim.World)) { b.world.Change(fn) }
func (b *bench) later(d time.Duration)        { b.clock.add(d) }

// stored returns what the store holds, bypassing the engine.
func (b *bench) stored() []*fact.Fact {
	b.t.Helper()
	all, err := b.store.Facts()
	if err != nil {
		b.t.Fatal(err)
	}
	return all
}

func (b *bench) get(id string) *fact.Fact {
	b.t.Helper()
	f, err := b.store.Get(id)
	if err != nil {
		b.t.Fatalf("the store has no %s: %v", id, err)
	}
	return f
}

// refusal fails the test unless err is a refusal, and returns its words.
func refusal(t *testing.T, err error) string {
	t.Helper()
	var r *Refused
	if !errors.As(err, &r) {
		t.Fatalf("got the error %v (%T), want a *Refused", err, err)
	}
	if r.Why == "" || r.Error() != r.Why {
		t.Fatalf("a refusal must say why: %q / %q", r.Why, r.Error())
	}
	return r.Why
}

func is(t *testing.T, got *time.Time, want time.Time, what string) {
	t.Helper()
	if got == nil || !got.Equal(want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func ids(views []View) []string {
	out := make([]string, len(views))
	for i, v := range views {
		out[i] = v.ID
	}
	return out
}

func claims(views []View) string {
	out := make([]string, len(views))
	for i, v := range views {
		out[i] = v.Claim
	}
	return strings.Join(out, " | ")
}

func TestRememberStoresAFactWhoseCheckPasses(t *testing.T) {
	b := newBench(t)
	v, already, err := b.Remember(context.Background(), Claim{
		Claim: "  The project targets\nGo 1.24 ",
		Check: `  contains go.mod "go 1.24"  `,
		Tags:  []string{"Build", "#toolchain, go"},
		By:    "claude",
	})
	if err != nil || already {
		t.Fatalf("Remember = %v, already %v", err, already)
	}
	if !fact.ValidID(v.ID) {
		t.Errorf("the id %q is not a fact id", v.ID)
	}
	if v.Claim != "The project targets Go 1.24" {
		t.Errorf("claim = %q, want it on one line and trimmed", v.Claim)
	}
	if v.Status != fact.Holds || !v.Fresh || v.Late {
		t.Errorf("status %s, fresh %v, late %v; want holds, proved in this very call", v.Status, v.Fresh, v.Late)
	}
	if want := `go.mod contains "go 1.24" (line 3)`; v.Detail != want {
		t.Errorf("detail = %q, want %q", v.Detail, want)
	}
	if v.Check != `contains go.mod "go 1.24"` || v.Kind != "contains" {
		t.Errorf("check = %q, kind = %q", v.Check, v.Kind)
	}
	if strings.Join(v.Tags, " ") != "build toolchain go" || v.By != "claude" {
		t.Errorf("tags = %v, by = %q", v.Tags, v.By)
	}
	if !v.Created.Equal(start) {
		t.Errorf("created = %v, want %v", v.Created, start)
	}
	is(t, v.CheckedAt, start, "checked_at")
	is(t, v.Since, start, "since")
	is(t, v.Held, start, "held")
	if v.NotedAt != nil || v.StaleAt != nil {
		t.Errorf("a fact with a check does not go stale, yet noted_at = %v, stale_at = %v", v.NotedAt, v.StaleAt)
	}

	all := b.stored()
	if len(all) != 1 || all[0].ID != v.ID {
		t.Fatalf("the store holds %d facts, want the one just remembered", len(all))
	}
	if all[0].Last == nil || all[0].Last.Status != fact.Holds || all[0].Last.Detail != v.Detail {
		t.Errorf("the proof was not stored with the fact: %+v", all[0].Last)
	}
}

func TestRememberRefusesAFactWhoseCheckFails(t *testing.T) {
	tests := []struct {
		name   string
		check  string
		detail string
	}{
		{"a file that is not there", "file missing.txt", "missing.txt does not exist"},
		{"text the file does not have", `contains go.mod "go 1.19"`, `go.mod does not contain "go 1.19"`},
		{"a port nothing listens on", "port localhost:9999", "localhost:9999 does not accept connections: connection refused"},
		{"the opposite of something true", "not file go.mod", "go.mod exists"},
		{"a command that prints something else", "says v18 node --version", `node --version exited 0 but did not print "v18" (it printed: v22.3.0)`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t)
			_, _, err := b.Remember(context.Background(), Claim{Claim: "Something that is not so", Check: tc.check, By: "claude"})
			want := "the check fails right now, so this is not a fact yet: " + tc.detail +
				`. Correct the claim or the check; or, to record that something is not so, start the check with "not".`
			if got := refusal(t, err); got != want {
				t.Errorf("refused with\n  %s\nwant\n  %s", got, want)
			}
			if n := len(b.stored()); n != 0 {
				t.Errorf("a refused fact was stored all the same (%d facts)", n)
			}
		})
	}
}

// The refusal tells the agent what to do; doing it must work.
func TestRememberAcceptsThatSomethingIsNotSoWhenTheCheckStartsWithNot(t *testing.T) {
	b := newBench(t)
	v := b.remember("There is no .env file in the repository", "not file .env")
	if v.Status != fact.Holds || v.Detail != ".env does not exist" || v.Check != "not file .env" {
		t.Errorf("status %s, detail %q, check %q", v.Status, v.Detail, v.Check)
	}
}

func TestRememberStoresANoteWhenThereIsNoCheck(t *testing.T) {
	tests := []struct {
		name string
		ttl  string
		life time.Duration
	}{
		{"with the usual thirty days to live", "", 30 * 24 * time.Hour},
		{"with a time to live of its own", "7d", 7 * 24 * time.Hour},
		{"with spaces around the time to live", " 2w ", 14 * 24 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t)
			v, already, err := b.Remember(context.Background(), Claim{Claim: "Deploys happen on Fridays", TTL: tc.ttl, By: "claude"})
			if err != nil || already {
				t.Fatalf("Remember = %v, already %v", err, already)
			}
			if v.Status != fact.Unproven || v.Fresh {
				t.Errorf("status %s, fresh %v; a note is unproven and nothing ran", v.Status, v.Fresh)
			}
			if v.Check != "" || v.Kind != "" || v.Detail != "" || v.CheckedAt != nil || v.Since != nil || v.Held != nil {
				t.Errorf("a note carries proof it cannot have: %+v", v)
			}
			is(t, v.NotedAt, start, "noted_at")
			is(t, v.StaleAt, start.Add(tc.life), "stale_at")
			if got := b.get(v.ID); got.Check != nil || got.Last != nil || got.TimeToLive() != tc.life {
				t.Errorf("stored as %+v with %v to live", got, got.TimeToLive())
			}
		})
	}
}

func TestRememberStoresACheckThatMayNotRunAsBlocked(t *testing.T) {
	tests := []struct {
		name   string
		check  string
		detail string
	}{
		{"a file outside the allowed folders", "file /etc/hosts",
			"/etc/hosts is outside the folders the policy allows. A person can allow it: holdfast allow files /etc"},
		{"a file above the project", "contains ../other/go.mod x",
			"../other/go.mod is outside the folders the policy allows. A person can allow it: holdfast allow files ../other"},
		{"a host the policy does not name", "port db.internal:5432",
			"the policy does not allow connections to db.internal. A person can allow it: holdfast allow net db.internal"},
		{"a web address the policy does not name", "http 200 https://example.com/health",
			"the policy does not allow connections to example.com. A person can allow it: holdfast allow net example.com"},
		{"a command nobody allowed", "cmd git push --force",
			"the policy does not allow the command: git push --force. A person can allow it: holdfast allow cmd git push --force"},
		{"an allowed command with other arguments", "cmd git status",
			"the policy does not allow the command: git status. A person can allow it: holdfast allow cmd git status"},
		// Allowed, but this machine does not have it: nothing was proved
		// either way.
		{"an allowed command that cannot be run", "cmd make test",
			"could not check: make test could not be run: make: executable file not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t)
			v := b.remember("Something only a person can let Holdfast check", tc.check)
			if v.Status != fact.Blocked {
				t.Fatalf("status = %s, want blocked", v.Status)
			}
			if v.Detail != tc.detail {
				t.Errorf("detail =\n  %s\nwant\n  %s", v.Detail, tc.detail)
			}
			if !v.Fresh || v.Held != nil {
				t.Errorf("fresh %v, held %v: the attempt was made now, and the fact has never held", v.Fresh, v.Held)
			}
			if got := b.get(v.ID); got.Last == nil || got.Last.Status != fact.Blocked {
				t.Errorf("stored with the evidence %+v, want blocked", got.Last)
			}
		})
	}
}

func TestRememberReturnsTheFactAlreadyThereForTheSameClaimAndCheck(t *testing.T) {
	b := newBench(t)
	first := b.remember("The API listens on port 8080", "port localhost:8080", "api")
	note := b.remember("Deploys happen on Fridays", "")

	// The world moves on: if the check ran again now, it would fail.
	b.change(func(w *sim.World) { w.Ports["localhost:8080"] = false })
	b.later(time.Hour)

	again, already, err := b.Remember(context.Background(), Claim{Claim: "the api LISTENS on port 8080", Check: " port  localhost:8080 ", By: "someone else"})
	if err != nil || !already {
		t.Fatalf("the same fact again = %v, already %v; want the existing fact", err, already)
	}
	if again.ID != first.ID || again.By != "claude" || again.Fresh {
		t.Errorf("got %s by %q (fresh %v), want the stored fact %s untouched", again.ID, again.By, again.Fresh, first.ID)
	}
	if again.Status != fact.Holds {
		t.Errorf("status = %s: handing back a stored fact must not run its check or change it", again.Status)
	}
	if v, already, err := b.Remember(context.Background(), Claim{Claim: "Deploys happen on Fridays"}); err != nil || !already || v.ID != note.ID {
		t.Errorf("the same note again = %s, already %v, %v; want %s", v.ID, already, err, note.ID)
	}
	if n := len(b.stored()); n != 2 {
		t.Fatalf("the store holds %d facts after two repeats, want 2", n)
	}

	b.change(func(w *sim.World) { w.Ports["localhost:8080"] = true })
	different := []Claim{
		{Claim: "The API listens on port 8080", Check: `contains config/app.yaml "port: 8080"`}, // another check
		{Claim: "The API listens on port 8080"},                                                 // no check at all
		{Claim: "The web server listens on port 8080", Check: "port localhost:8080"},            // another claim
		{Claim: "Deploys happen on Fridays", Check: "file go.mod"},                              // the note, now with a check
	}
	for _, c := range different {
		if _, already, err := b.Remember(context.Background(), c); err != nil || already {
			t.Errorf("Remember(%q, %q) = already %v, %v; want a new fact", c.Claim, c.Check, already, err)
		}
	}
	if n := len(b.stored()); n != 6 {
		t.Errorf("the store holds %d facts, want 6", n)
	}
}

func TestRememberDoesNotMistakeAWithdrawnFactForTheSameFact(t *testing.T) {
	b := newBench(t)
	old := b.remember("The API listens on port 8080", "port localhost:8080")
	if _, err := b.Retract(old.ID, "testing", "alex"); err != nil {
		t.Fatal(err)
	}
	v, already, err := b.Remember(context.Background(), Claim{Claim: "The API listens on port 8080", Check: "port localhost:8080"})
	if err != nil || already || v.ID == old.ID {
		t.Errorf("after the retraction, Remember = %s, already %v, %v; want a new fact", v.ID, already, err)
	}
}

// "unchanged" compares the file with what it was when the fact was stored. A
// repeat is the same fact even after the file has changed: the digest is part
// of the proof, not of the question.
func TestRememberSeesTheSameUnchangedCheckWhateverTheFileHoldsNow(t *testing.T) {
	b := newBench(t)
	first := b.remember("The lock file is the reviewed one", "unchanged go.sum")
	b.change(func(w *sim.World) { w.Write("go.sum", "something else\n", start) })
	v, already, err := b.Remember(context.Background(), Claim{Claim: "The lock file is the reviewed one", Check: "unchanged go.sum"})
	if err != nil || !already || v.ID != first.ID {
		t.Errorf("Remember = %s, already %v, %v; want the stored fact %s", v.ID, already, err, first.ID)
	}
}

func TestRememberTakesTheFingerprintItselfAndNeverFromTheCaller(t *testing.T) {
	b := newBench(t)
	sum := sha256.Sum256([]byte("example.com/dep v1.0.0 h1:abc=\n"))
	real := hex.EncodeToString(sum[:])
	forged := strings.Repeat("0", 64)

	v := b.remember("The lock file is the reviewed one", "unchanged go.sum "+forged)
	if v.Status != fact.Holds {
		t.Fatalf("status = %s (%s)", v.Status, v.Detail)
	}
	if got := b.get(v.ID).Check.Sum; got != real {
		t.Errorf("the stored digest is %s, want the file's own %s", got, real)
	}
	if v.Check != "unchanged go.sum" {
		t.Errorf("the check is shown as %q, want it without the digest", v.Check)
	}
	want := "go.sum is unchanged (sha256 " + real[:8] + "…" + real[len(real)-4:] + ")"
	if v.Detail != want {
		t.Errorf("detail = %q, want %q", v.Detail, want)
	}
}

func TestRememberCannotKeepAFileUnchangedThatIsNotThere(t *testing.T) {
	b := newBench(t)
	_, _, err := b.Remember(context.Background(), Claim{Claim: "The licence has not been touched", Check: "unchanged LICENSE"})
	if got, want := refusal(t, err), "cannot store this: LICENSE does not exist, so there is nothing to keep unchanged"; got != want {
		t.Errorf("refused with %q, want %q", got, want)
	}
	// Outside the policy, the digest cannot be taken either.
	_, _, err = b.Remember(context.Background(), Claim{Claim: "The hosts file has not been touched", Check: "unchanged /etc/hosts"})
	if got := refusal(t, err); !strings.HasPrefix(got, "cannot store this: /etc/hosts is outside the folders the policy allows") {
		t.Errorf("refused with %q", got)
	}
	if n := len(b.stored()); n != 0 {
		t.Errorf("%d facts stored", n)
	}
}

func TestRememberReplacesAnOlderFact(t *testing.T) {
	b := newBench(t)
	old := b.remember("The API listens on port 8080", "port localhost:8080")
	b.change(func(w *sim.World) { w.Ports["localhost:8080"], w.Ports["localhost:9090"] = false, true })
	b.later(time.Hour)

	v, already, err := b.Remember(context.Background(), Claim{
		Claim: "The API listens on port 9090", Check: "port localhost:9090", Replaces: " " + old.ID + " ", By: "claude",
	})
	if err != nil || already {
		t.Fatalf("Remember = %v, already %v", err, already)
	}
	if v.Replaces != old.ID || v.Status != fact.Holds {
		t.Errorf("the new fact: replaces %q, status %s", v.Replaces, v.Status)
	}
	was := b.get(old.ID)
	if was.ReplacedBy != v.ID || was.Live() {
		t.Errorf("the old fact: replaced_by %q, live %v", was.ReplacedBy, was.Live())
	}
	if was.Retracted != nil {
		t.Error("the old fact was marked retracted; it was replaced, which is a different thing")
	}
	r, err := b.Recall(context.Background(), "port", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Facts) != 1 || r.Facts[0].ID != v.ID || r.Known != 1 {
		t.Errorf("recall returns %v (known %d), want only the new fact", ids(r.Facts), r.Known)
	}
}

func TestRememberRefusesToReplaceWhatCannotBeReplaced(t *testing.T) {
	b := newBench(t)
	retracted := b.remember("A fact that will be withdrawn", "file go.mod")
	if _, err := b.Retract(retracted.ID, "testing", "alex"); err != nil {
		t.Fatal(err)
	}
	replaced := b.remember("A fact that will be replaced", "file go.sum")
	newer, _, err := b.Remember(context.Background(), Claim{Claim: "The fact that replaced it", Check: "file go.sum", Replaces: replaced.ID})
	if err != nil {
		t.Fatal(err)
	}
	live := b.remember("A fact that stays", "file config/app.yaml")
	before := len(b.stored())

	tests := []struct {
		name     string
		replaces string
		check    string
		want     string
	}{
		{"an id nobody has", "f-222222", "", "f-222222 cannot be replaced: there is no fact with that id"},
		{"something that is not an id", "seven", "", `"seven" is not a fact id; ids look like f-abc234`},
		{"an id in capitals", strings.ToUpper(live.ID), "", `"` + strings.ToUpper(live.ID) + `" is not a fact id; ids look like f-abc234`},
		{"a fact already retracted", retracted.ID, "", retracted.ID + " was already retracted"},
		{"a fact already replaced", replaced.ID, "", replaced.ID + " was already replaced by " + newer.ID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := b.Remember(context.Background(), Claim{Claim: "A replacement for " + tc.name, Check: tc.check, Replaces: tc.replaces})
			if got := refusal(t, err); got != tc.want {
				t.Errorf("refused with %q, want %q", got, tc.want)
			}
		})
	}
	// A replacement whose own check fails replaces nothing.
	_, _, err = b.Remember(context.Background(), Claim{Claim: "A replacement that is not true", Check: "file missing.txt", Replaces: live.ID})
	refusal(t, err)
	if got := b.get(live.ID); !got.Live() {
		t.Error("a refused replacement still withdrew the fact it was meant to replace")
	}
	if n := len(b.stored()); n != before {
		t.Errorf("refused replacements stored %d facts", n-before)
	}
}

func TestRememberRefusesWhatIsNotAWellFormedFact(t *testing.T) {
	long := strings.Repeat("x", fact.MaxCheck-len("file ")+1)
	tests := []struct {
		name string
		in   Claim
		want string // the whole refusal, or its beginning when it ends with "..."
	}{
		{"no claim", Claim{Check: "file go.mod"}, "the claim is empty: say the fact in one plain sentence"},
		{"a claim of nothing but space", Claim{Claim: " \n\t "}, "the claim is empty: say the fact in one plain sentence"},
		{"a claim that is too long", Claim{Claim: strings.Repeat("é", fact.MaxClaim+1)}, "the claim is 401 characters; keep it under 400 and put detail in the check"},
		{"a tag with odd characters", Claim{Claim: "x", Tags: []string{"no!good"}}, `tag "no!good": use letters, digits, '-', '_', '.' or '/'`},
		{"a tag that is too long", Claim{Claim: "x", Tags: []string{strings.Repeat("t", fact.MaxTag+1)}}, `tag "` + strings.Repeat("t", fact.MaxTag+1) + `" is longer than 32 characters`},
		{"too many tags", Claim{Claim: "x", Tags: []string{"a b c d e f g h i"}}, "9 tags; the limit is 8"},
		{"a time to live that is not one", Claim{Claim: "x", TTL: "soon"}, `duration "soon"...`},
		{"a check of an unknown kind", Claim{Claim: "x", Check: "teleport home"}, "..."},
		{"a check with too few words", Claim{Claim: "x", Check: "contains go.mod"}, "..."},
		{"a check that is too long", Claim{Claim: "x", Check: "file " + long}, "the check is 1001 characters; the limit is 1000"},
		{"a regular expression that does not compile", Claim{Claim: "x", Check: `matches go.mod "go 1.(24"`}, "..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t)
			tc.in.By = "claude"
			_, already, err := b.Remember(context.Background(), tc.in)
			got := refusal(t, err)
			if prefix, open := strings.CutSuffix(tc.want, "..."); open {
				if !strings.HasPrefix(got, prefix) {
					t.Errorf("refused with %q, want it to begin %q", got, prefix)
				}
			} else if got != tc.want {
				t.Errorf("refused with %q, want %q", got, tc.want)
			}
			if already || len(b.stored()) != 0 {
				t.Errorf("already = %v and %d facts stored after a refusal", already, len(b.stored()))
			}
		})
	}
}

func TestRememberKeepsTheAuthorShortAndOnOneLine(t *testing.T) {
	b := newBench(t)
	v, _, err := b.Remember(context.Background(), Claim{Claim: "A note", By: "an agent\nwith a very long name " + strings.Repeat("x", 100)})
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(v.By)); n > fact.MaxAuthor || strings.ContainsAny(v.By, "\n\r") || !strings.HasPrefix(v.By, "an agent with a very long name") {
		t.Errorf("by = %q (%d characters)", v.By, n)
	}
}

// A store that fills up says so in words for whoever asked, not as a fault.
func TestRememberPassesOnTheStoresOwnRefusal(t *testing.T) {
	b := newBench(t)
	b.Store = full{b.store}
	_, _, err := b.Remember(context.Background(), Claim{Claim: "One more"})
	if got := refusal(t, err); got != "this store already holds 2000 facts; retract some before adding more" {
		t.Errorf("refused with %q", got)
	}
}

type full struct{ store.Store }

func (full) Add(*fact.Fact, *fact.Evidence) (*fact.Fact, error) {
	return nil, &store.Rejected{Why: "this store already holds 2000 facts; retract some before adding more"}
}

// A store that fails is a fault, and must not be dressed up as a refusal.
func TestAFailingStoreIsAFaultNotARefusal(t *testing.T) {
	b := newBench(t)
	v := b.remember("The API listens on port 8080", "port localhost:8080")
	boom := errors.New("disk on fire")
	b.Store = failing{b.store, boom}
	b.later(time.Hour)

	var r *Refused
	check := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, boom) || errors.As(err, &r) {
			t.Errorf("%s = %v; want the store's own error, and not as a refusal", what, err)
		}
	}
	_, _, err := b.Remember(context.Background(), Claim{Claim: "Another"})
	check("Remember", err)
	_, err = b.Recall(context.Background(), "", 0)
	check("Recall", err)
	_, err = b.Verify(context.Background(), nil)
	check("Verify", err)
	_, err = b.List(false)
	check("List", err)
	_, err = b.Get(v.ID)
	check("Get", err)
	_, err = b.Retract(v.ID, "why", "alex")
	check("Retract", err)
	_, err = b.Vouch(v.ID, "alex")
	check("Vouch", err)
}

type failing struct {
	store.Store
	err error
}

func (f failing) Facts() ([]*fact.Fact, error)          { return nil, f.err }
func (f failing) Get(string) (*fact.Fact, error)        { return nil, f.err }
func (f failing) Record([]store.Proof) error            { return f.err }
func (f failing) Retract(string, fact.Retraction) error { return f.err }

// stuck is a machine whose network never answers: a connection attempt hangs
// until whoever asked stops waiting.
type stuck struct {
	*sim.World
	started chan struct{}
}

func (w *stuck) Dial(ctx context.Context, addr string, guard world.Guard) error {
	select {
	case w.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

// A caller that stops waiting (a cancelled request, a shutdown) cuts the check
// short. Nothing was proved either way, so nothing may be stored: not as
// proved, and not as "blocked until a person allows it" either.
func TestRememberStoresNothingWhenItsCheckWasCutShort(t *testing.T) {
	for _, check := range []string{"port localhost:8080", "not port localhost:8080"} {
		t.Run(check, func(t *testing.T) {
			b := newBench(t)
			hung := &stuck{World: b.world, started: make(chan struct{}, 1)}
			b.World = hung
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				<-hung.started
				cancel()
			}()
			v, _, err := b.Remember(ctx, Claim{Claim: "The API listens, or does not", Check: check, By: "claude"})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("a remember that was cut short answered %v (%s, %q); want the caller's own cancellation", err, v.Status, v.Detail)
			}
			var r *Refused
			if errors.As(err, &r) {
				t.Errorf("it was refused (%q), as if the claim had been found false", r.Why)
			}
			if all := b.stored(); len(all) != 0 {
				t.Errorf("a fact was stored from a check that never finished: %s, %+v", all[0].Claim, all[0].Last)
			}
		})
	}
}

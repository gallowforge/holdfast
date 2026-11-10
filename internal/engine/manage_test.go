package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/sim"
)

const day = 24 * time.Hour

func (b *bench) list(gone bool) Listed {
	b.t.Helper()
	l, err := b.List(gone)
	if err != nil {
		b.t.Fatalf("List(%v): %v", gone, err)
	}
	return l
}

func (b *bench) view(id string) View {
	b.t.Helper()
	v, err := b.Get(id)
	if err != nil {
		b.t.Fatalf("Get(%s): %v", id, err)
	}
	return v
}

func TestRetractNeedsAReason(t *testing.T) {
	b := newBench(t)
	v := b.remember("The API listens on port 8080", "port localhost:8080")
	for _, reason := range []string{"", "   ", "\n\t", "\x00\x07"} {
		_, err := b.Retract(v.ID, reason, "alex")
		want := "say why " + v.ID + " is being retracted, in a few words: the reason is kept with it"
		if got := refusal(t, err); got != want {
			t.Errorf("Retract with the reason %q refused with %q, want %q", reason, got, want)
		}
	}
	if kept := b.get(v.ID); !kept.Live() {
		t.Error("the fact was withdrawn although every retraction was refused")
	}
}

func TestRetractKeepsWhoWithdrewTheFactAndWhy(t *testing.T) {
	b := newBench(t)
	v := b.remember("The API listens on port 8080", "port localhost:8080")
	b.later(3 * time.Hour)

	got, err := b.Retract(" "+v.ID+"\n", "  the port moved\nto 9090 ", "alex")
	if err != nil {
		t.Fatal(err)
	}
	when := start.Add(3 * time.Hour)
	if got.ID != v.ID || got.Retracted == nil {
		t.Fatalf("Retract returned %+v", got)
	}
	if r := got.Retracted; !r.At.Equal(when) || r.By != "alex" || r.Reason != "the port moved to 9090" {
		t.Errorf("retraction = %+v, want it at %v by alex with the reason on one line", r, when)
	}
	if kept := b.get(v.ID); kept.Live() || kept.Retracted == nil || kept.Retracted.Reason != "the port moved to 9090" {
		t.Errorf("the store keeps %+v", kept.Retracted)
	}
	// It stays in the store, and its proof with it.
	if kept := b.get(v.ID); kept.Last == nil || kept.Claim != v.Claim {
		t.Errorf("the retracted fact lost what it was: %+v", kept)
	}
	want := "RETRACTED " + v.ID + "  The API listens on port 8080\n" +
		"          retracted just now by alex: the port moved to 9090"
	if text := got.Text(b.clock.now()); text != want {
		t.Errorf("text =\n%s\nwant\n%s", text, want)
	}

	_, err = b.Retract(v.ID, "once more", "alex")
	if got, want := refusal(t, err), v.ID+" was already retracted"; got != want {
		t.Errorf("a second retraction refused with %q, want %q", got, want)
	}
}

func TestRetractKeepsTheReasonAndTheNameShort(t *testing.T) {
	b := newBench(t)
	v := b.remember("A note", "")
	got, err := b.Retract(v.ID, strings.Repeat("because ", 100), strings.Repeat("n", 200))
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(got.Retracted.Reason)); n > fact.MaxReason {
		t.Errorf("the reason is %d characters, more than %d", n, fact.MaxReason)
	}
	if n := len([]rune(got.Retracted.By)); n > fact.MaxAuthor {
		t.Errorf("the name is %d characters, more than %d", n, fact.MaxAuthor)
	}
}

func TestRetractRefusesAnIDItDoesNotKnow(t *testing.T) {
	b := newBench(t)
	for _, tc := range []struct{ id, shown string }{{"f-222222", "f-222222"}, {"", ""}, {" what ", "what"}} {
		_, err := b.Retract(tc.id, "a reason", "alex")
		if got, want := refusal(t, err), fmt.Sprintf("there is no fact with the id %q", tc.shown); got != want {
			t.Errorf("Retract(%q) refused with %q, want %q", tc.id, got, want)
		}
	}
}

// A fact that was replaced can still be retracted: "replaced" says a better
// one exists, "retracted" says this one should never have been believed.
func TestAReplacedFactCanStillBeRetracted(t *testing.T) {
	b := newBench(t)
	old := b.remember("The API listens on port 8080", "port localhost:8080")
	if _, _, err := b.Remember(context.Background(), Claim{Claim: "The config sets the port", Check: `contains config/app.yaml "port: 8080"`, Replaces: old.ID}); err != nil {
		t.Fatal(err)
	}
	got, err := b.Retract(old.ID, "it was wrong all along", "alex")
	if err != nil {
		t.Fatalf("Retract of a replaced fact: %v", err)
	}
	if got.Retracted == nil || got.ReplacedBy == "" {
		t.Errorf("retracted %v, replaced by %q; want both kept", got.Retracted, got.ReplacedBy)
	}
	if text := got.Text(b.clock.now()); !strings.HasPrefix(text, "RETRACTED "+old.ID) {
		t.Errorf("text = %q; the retraction is what is shown", text)
	}
}

func TestVouchRestartsTheLifeOfANote(t *testing.T) {
	b := newBench(t)
	v := b.remember("Deploys happen on Fridays", "")
	b.later(41 * day)
	if got := b.view(v.ID); got.Status != fact.Stale {
		t.Fatalf("after 41 days the note is %s, want stale", got.Status)
	}

	got, err := b.Vouch(" "+v.ID+" ", "alex")
	if err != nil {
		t.Fatal(err)
	}
	when := start.Add(41 * day)
	if got.Status != fact.Unproven || got.VouchedBy != "alex" {
		t.Errorf("after vouching: %s, vouched by %q; want unproven, by alex", got.Status, got.VouchedBy)
	}
	is(t, got.NotedAt, when, "noted_at")
	is(t, got.StaleAt, when.Add(30*day), "stale_at")
	if !got.Created.Equal(start) || got.By != "claude" {
		t.Errorf("vouching changed who stored the note and when: %v by %q", got.Created, got.By)
	}
	want := "UNPROVEN  " + v.ID + "  Deploys happen on Fridays\n" +
		"          a note by claude, confirmed by alex, under a minute old; it has no check, so nothing proves it"
	if text := got.Text(b.clock.now()); text != want {
		t.Errorf("text =\n%s\nwant\n%s", text, want)
	}

	// The new life runs out like the first one did.
	b.later(30 * day)
	if got := b.view(v.ID); got.Status != fact.Unproven {
		t.Errorf("exactly 30 days after vouching the note is %s, want still unproven", got.Status)
	}
	b.later(time.Second)
	if got := b.view(v.ID); got.Status != fact.Stale {
		t.Errorf("past 30 days after vouching the note is %s, want stale", got.Status)
	}
}

func TestVouchIsOnlyForLiveFactsWithoutACheck(t *testing.T) {
	b := newBench(t)
	proved := b.remember("The API listens on port 8080", "port localhost:8080")
	blocked := b.remember("The hosts file is in place", "file /etc/hosts")
	retracted := b.remember("Deploys happen on Mondays", "")
	if _, err := b.Retract(retracted.ID, "they do not", "alex"); err != nil {
		t.Fatal(err)
	}
	replaced := b.remember("Deploys happen on Tuesdays", "")
	if _, _, err := b.Remember(context.Background(), Claim{Claim: "Deploys happen on Fridays", Replaces: replaced.ID}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		id   string
		want string
	}{
		{"a fact that proves itself", proved.ID, proved.ID + " has a check, so it proves itself; there is nothing to vouch for"},
		{"a fact whose check is blocked", blocked.ID, blocked.ID + " has a check, so it proves itself; there is nothing to vouch for"},
		{"a retracted note", retracted.ID, retracted.ID + " has been retracted or replaced"},
		{"a replaced note", replaced.ID, replaced.ID + " has been retracted or replaced"},
		{"an id nobody has", "f-222222", `there is no fact with the id "f-222222"`},
		{"no id", "  ", `there is no fact with the id ""`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := b.Vouch(tc.id, "alex")
			if got := refusal(t, err); got != tc.want {
				t.Errorf("refused with %q, want %q", got, tc.want)
			}
		})
	}
	for _, f := range b.stored() {
		if f.VouchedBy != "" || !f.Vouched.IsZero() {
			t.Errorf("%s was vouched for although every vouch was refused", f.ID)
		}
	}
}

func TestGetShowsAFactAsLastKnownWithoutRunningItsCheck(t *testing.T) {
	b := newBench(t)
	v := b.remember("The API listens on port 8080", "port localhost:8080", "api")
	b.later(time.Hour)
	b.change(func(w *sim.World) { w.Ports["localhost:8080"] = false })

	got := b.view("  " + v.ID + "\n")
	if got.Status != fact.Holds || got.Fresh || got.Late {
		t.Errorf("status %s, fresh %v, late %v; Get reports what was last seen and runs nothing", got.Status, got.Fresh, got.Late)
	}
	is(t, got.CheckedAt, start, "checked_at")
	if got.ID != v.ID || got.Claim != v.Claim || got.Check != "port localhost:8080" || len(got.History) != 1 {
		t.Errorf("Get returned %+v", got)
	}
	if kept := b.get(v.ID); !kept.Last.At.Equal(start) {
		t.Error("Get ran the check")
	}

	_, err := b.Get("f-222222")
	if got, want := refusal(t, err), `there is no fact with the id "f-222222"`; got != want {
		t.Errorf("refused with %q, want %q", got, want)
	}

	// A withdrawn fact can still be looked at: that is how one asks why.
	if _, err := b.Retract(v.ID, "the port moved", "alex"); err != nil {
		t.Fatal(err)
	}
	if got := b.view(v.ID); got.Retracted == nil || got.Retracted.Reason != "the port moved" {
		t.Errorf("Get of a retracted fact: %+v", got.Retracted)
	}
}

func TestANoteGoesStaleOnlyOnceItsLifeIsOver(t *testing.T) {
	tests := []struct {
		name  string
		ttl   string
		after time.Duration
		want  fact.Status
	}{
		{"new", "", 0, fact.Unproven},
		{"a day short of thirty", "", 29 * day, fact.Unproven},
		{"at thirty days to the second", "", 30 * day, fact.Unproven},
		{"a moment past thirty days", "", 30*day + time.Millisecond, fact.Stale},
		{"an hour's life, half spent", "1h", 30 * time.Minute, fact.Unproven},
		{"an hour's life, spent", "1h", 61 * time.Minute, fact.Stale},
		{"a year's life after thirty days", "52w", 31 * day, fact.Unproven},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t)
			v, _, err := b.Remember(context.Background(), Claim{Claim: "Deploys happen on Fridays", TTL: tc.ttl})
			if err != nil {
				t.Fatal(err)
			}
			b.later(tc.after)
			if got := b.view(v.ID); got.Status != tc.want {
				t.Errorf("Get: %s, want %s", got.Status, tc.want)
			}
			if got := b.list(false).Facts[0]; got.Status != tc.want {
				t.Errorf("List: %s, want %s", got.Status, tc.want)
			}
			if got := b.recall("", 0).Facts[0]; got.Status != tc.want {
				t.Errorf("Recall: %s, want %s", got.Status, tc.want)
			}
		})
	}
}

// A fact with a check never goes stale: its check is what keeps it honest.
func TestAFactWithACheckDoesNotGoStale(t *testing.T) {
	b := newBench(t)
	v := b.remember("The API listens on port 8080", "port localhost:8080")
	b.later(400 * day)
	if got := b.view(v.ID); got.Status != fact.Holds || got.StaleAt != nil {
		t.Errorf("after 400 days: %s, stale_at %v", got.Status, got.StaleAt)
	}
}

func TestListPutsWhatNeedsAttentionFirst(t *testing.T) {
	b := newBench(t)
	port := b.remember("The API listens on port 8080", "port localhost:8080")
	sum := b.remember("The project has a lock file", "file go.sum")
	mod := b.remember("The project is a Go module", "file go.mod")
	hosts := b.remember("The hosts file is in place", "file /etc/hosts")
	old, _, err := b.Remember(context.Background(), Claim{Claim: "The freeze ends within the hour", TTL: "1h", By: "claude"})
	if err != nil {
		t.Fatal(err)
	}

	b.later(2 * time.Hour) // the one-hour note is stale from here on
	goVersion := b.remember("The project targets Go 1.24", `contains go.mod "go 1.24"`)
	note := b.remember("Deploys happen on Fridays", "")
	b.change(func(w *sim.World) { w.Ports["localhost:8080"] = false })
	b.verify(port.ID)
	b.later(time.Minute)
	b.change(func(w *sim.World) { delete(w.Files, "go.sum") })
	b.verify(sum.ID)
	b.later(time.Minute)

	l := b.list(false)
	want := []struct {
		id     string
		status fact.Status
		why    string
	}{
		{sum.ID, fact.Broken, "broken, and the latest to break"},
		{port.ID, fact.Broken, "broken a minute earlier"},
		{hosts.ID, fact.Blocked, "blocked"},
		{old.ID, fact.Stale, "a stale note"},
		{note.ID, fact.Unproven, "a note"},
		{goVersion.ID, fact.Holds, "holds, and the latest to be proved"},
		{mod.ID, fact.Holds, "holds since the start"},
	}
	if len(l.Facts) != len(want) {
		t.Fatalf("%d facts listed, want %d", len(l.Facts), len(want))
	}
	for i, w := range want {
		if got := l.Facts[i]; got.ID != w.id || got.Status != w.status {
			t.Errorf("place %d: %s %q, want %s (%s)", i+1, got.Status, got.Claim, w.status, w.why)
		}
	}
	if want := (Counts{Total: 7, Holds: 2, Broken: 2, Blocked: 1, Unproven: 1, Stale: 1}); l.Counts != want {
		t.Errorf("counts = %+v, want %+v", l.Counts, want)
	}
	if got, want := l.Counts.String(), "7 facts: 2 broken, 1 blocked, 1 stale, 1 unproven, 2 hold"; got != want {
		t.Errorf("counts read %q, want %q", got, want)
	}
	if l.Gone != nil {
		t.Errorf("gone = %v, though nobody asked for it", ids(l.Gone))
	}
	for _, v := range l.Facts {
		if v.Fresh || v.Late {
			t.Errorf("%s: fresh %v, late %v; a list runs nothing", v.ID, v.Fresh, v.Late)
		}
	}
}

func TestListRunsNoChecks(t *testing.T) {
	b := newBench(t)
	v := b.remember("The API listens on port 8080", "port localhost:8080")
	b.later(time.Hour)
	b.change(func(w *sim.World) { w.Ports["localhost:8080"] = false })
	if got := b.list(true).Facts[0]; got.Status != fact.Holds {
		t.Errorf("listed as %s; a list shows what was last seen", got.Status)
	}
	if kept := b.get(v.ID); !kept.Last.At.Equal(start) || len(kept.History) != 1 {
		t.Errorf("List ran the check: %+v", kept.Last)
	}
}

func TestListShowsWithdrawnFactsOnlyWhenAsked(t *testing.T) {
	b := newBench(t)
	first := b.remember("Deploys happen on Mondays", "")
	b.later(time.Minute)
	second := b.remember("The API listens on port 8080", "port localhost:8080")
	b.later(time.Minute)
	third := b.remember("Deploys happen on Tuesdays", "")
	b.later(time.Minute)
	live := b.remember("The project is a Go module", "file go.mod")

	// Withdrawn in another order than they were stored.
	if _, err := b.Retract(third.ID, "they do not", "alex"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Retract(first.ID, "they do not", "alex"); err != nil {
		t.Fatal(err)
	}
	b.later(time.Minute)
	b.change(func(w *sim.World) { w.Ports["localhost:9090"] = true })
	newer, _, err := b.Remember(context.Background(), Claim{Claim: "The API listens on port 9090", Check: "port localhost:9090", Replaces: second.ID})
	if err != nil {
		t.Fatal(err)
	}

	without := b.list(false)
	if got, want := strings.Join(ids(without.Facts), " "), newer.ID+" "+live.ID; got != want || without.Gone != nil {
		t.Errorf("without the withdrawn ones: facts %s (want %s), gone %v", got, want, ids(without.Gone))
	}
	with := b.list(true)
	if got, want := strings.Join(ids(with.Facts), " "), newer.ID+" "+live.ID; got != want {
		t.Errorf("asking for the withdrawn ones changed the live ones: %s, want %s", got, want)
	}
	if got, want := strings.Join(ids(with.Gone), " "), third.ID+" "+second.ID+" "+first.ID; got != want {
		t.Errorf("gone = %s, want %s (the newest fact first)", got, want)
	}
	if with.Gone[0].Retracted == nil || with.Gone[1].ReplacedBy != newer.ID || with.Gone[1].Retracted != nil {
		t.Errorf("the withdrawn facts do not say how they went: %+v / %+v", with.Gone[0], with.Gone[1])
	}
	if with.Counts != without.Counts || with.Counts.Total != 2 {
		t.Errorf("counts = %+v with and %+v without; withdrawn facts are never counted", with.Counts, without.Counts)
	}
}

// Whoever reads the JSON can always loop over "facts": it is a list even when
// it is empty.
func TestListOfAnEmptyStoreIsAnEmptyListNotNothing(t *testing.T) {
	b := newBench(t)
	l := b.list(true)
	if l.Facts == nil || len(l.Facts) != 0 {
		t.Fatalf("facts = %#v, want an empty list", l.Facts)
	}
	raw, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), `{"facts":[],"counts":{"total":0,"holds":0,"broken":0,"blocked":0,"unproven":0,"stale":0}}`; got != want {
		t.Errorf("as JSON:\n  %s\nwant\n  %s", got, want)
	}
	if got := l.Text(start); got != "Nothing is remembered here yet." {
		t.Errorf("text = %q", got)
	}
}

// The view is the one shape every surface shows, so its JSON names are a
// promise to the web page and to agents.
func TestAViewHasTheSameNamesInJSONEverywhere(t *testing.T) {
	b := newBench(t)
	v := b.remember("The API listens on port 8080", "port localhost:8080", "api")
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"id", "claim", "tags", "check", "kind", "by", "created", "status", "detail", "checked_at", "since", "held", "history", "fresh"} {
		if _, ok := got[name]; !ok {
			t.Errorf("the JSON of a proved fact has no %q: %s", name, raw)
		}
	}
	if got["status"] != "holds" || got["check"] != "port localhost:8080" || got["kind"] != "port" {
		t.Errorf("status %v, check %v, kind %v", got["status"], got["check"], got["kind"])
	}
	for _, name := range []string{"noted_at", "stale_at", "retracted", "replaced_by", "replaces", "late", "vouched_by"} {
		if _, ok := got[name]; ok {
			t.Errorf("the JSON of a proved fact carries %q, which does not apply: %s", name, raw)
		}
	}

	note := b.remember("Deploys happen on Fridays", "")
	raw, _ = json.Marshal(note)
	got = map[string]any{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"noted_at", "stale_at"} {
		if _, ok := got[name]; !ok {
			t.Errorf("the JSON of a note has no %q: %s", name, raw)
		}
	}
	for _, name := range []string{"check", "kind", "detail", "checked_at", "since", "held", "fresh"} {
		if _, ok := got[name]; ok {
			t.Errorf("the JSON of a note carries %q: %s", name, raw)
		}
	}
}

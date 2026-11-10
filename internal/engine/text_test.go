package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/sim"
)

// The words below are read by agents and by people, and both act on them, so
// they are pinned down to the letter.

func ago(d time.Duration) *time.Time {
	t := start.Add(-d)
	return &t
}

const (
	refusedDetail = "localhost:8080 does not accept connections: connection refused"
	blockedDetail = "the policy does not allow connections to db.internal. A person can allow it: holdfast allow net db.internal"
)

func TestAFactAsText(t *testing.T) {
	tests := []struct {
		name string
		v    View
		want string
	}{
		{
			"one that holds",
			View{ID: "f-abc234", Claim: "The API listens on port 8080", Status: fact.Holds, Check: "port localhost:8080",
				Detail: "localhost:8080 accepts connections", CheckedAt: ago(time.Minute), Since: ago(time.Hour), Held: ago(time.Minute)},
			"HOLDS     f-abc234  The API listens on port 8080\n" +
				"          proof: localhost:8080 accepts connections · checked 1m ago",
		},
		{
			"one that was proved a moment ago",
			View{ID: "f-abc234", Claim: "The API listens on port 8080", Status: fact.Holds,
				Detail: "localhost:8080 accepts connections", CheckedAt: ago(0), Fresh: true},
			"HOLDS     f-abc234  The API listens on port 8080\n" +
				"          proof: localhost:8080 accepts connections · checked just now",
		},
		{
			"one that broke",
			View{ID: "f-abc234", Claim: "The API listens on port 8080", Status: fact.Broken, Check: "port localhost:8080",
				Detail: refusedDetail, CheckedAt: ago(12 * time.Second), Since: ago(12 * time.Second), Held: ago(3 * time.Hour)},
			"BROKEN    f-abc234  The API listens on port 8080\n" +
				"          proof failed: " + refusedDetail + " · checked 12s ago · it last held 3h ago\n" +
				"          check: port localhost:8080",
		},
		{
			"one that is broken and never held here",
			View{ID: "f-abc234", Claim: "The API listens on port 8080", Status: fact.Broken, Check: "port localhost:8080",
				Detail: refusedDetail, CheckedAt: ago(3 * day)},
			"BROKEN    f-abc234  The API listens on port 8080\n" +
				"          proof failed: " + refusedDetail + " · checked 3d ago\n" +
				"          check: port localhost:8080",
		},
		{
			"one whose check may not run",
			View{ID: "f-abc234", Claim: "The database accepts connections", Status: fact.Blocked, Check: "port db.internal:5432",
				Detail: blockedDetail, CheckedAt: ago(time.Minute)},
			"BLOCKED   f-abc234  The database accepts connections\n" +
				"          not checked: " + blockedDetail + "\n" +
				"          check: port db.internal:5432",
		},
		{
			"a note",
			View{ID: "f-abc234", Claim: "Deploys happen on Fridays", Status: fact.Unproven, By: "claude",
				Created: start.Add(-6 * day), NotedAt: ago(6 * day), StaleAt: ago(-24 * day)},
			"UNPROVEN  f-abc234  Deploys happen on Fridays\n" +
				"          a note by claude, 6d old; it has no check, so nothing proves it",
		},
		{
			"a note nobody signed",
			View{ID: "f-abc234", Claim: "Deploys happen on Fridays", Status: fact.Unproven,
				Created: start.Add(-20 * time.Second), NotedAt: ago(20 * time.Second)},
			"UNPROVEN  f-abc234  Deploys happen on Fridays\n" +
				"          a note, under a minute old; it has no check, so nothing proves it",
		},
		{
			"a note a person confirmed later",
			View{ID: "f-abc234", Claim: "Deploys happen on Fridays", Status: fact.Unproven, By: "claude", VouchedBy: "alex",
				Created: start.Add(-50 * day), NotedAt: ago(2 * time.Hour), StaleAt: ago(-30*day + 2*time.Hour)},
			"UNPROVEN  f-abc234  Deploys happen on Fridays\n" +
				"          a note by claude, confirmed by alex, 2h old; it has no check, so nothing proves it",
		},
		{
			"a stale note",
			View{ID: "f-abc234", Claim: "The refresh token lasts 30 days", Status: fact.Stale, By: "claude",
				Created: start.Add(-41 * day), NotedAt: ago(41 * day), StaleAt: ago(11 * day)},
			"STALE     f-abc234  The refresh token lasts 30 days\n" +
				"          a note by claude, 41d old, past its 30d life; nothing proves it, so treat it as a guess",
		},
		{
			"a stale note with a short life of its own",
			View{ID: "f-abc234", Claim: "The freeze ends within the hour", Status: fact.Stale,
				Created: start.Add(-3 * time.Hour), NotedAt: ago(3 * time.Hour), StaleAt: ago(2 * time.Hour)},
			"STALE     f-abc234  The freeze ends within the hour\n" +
				"          a note, 3h old, past its 1h life; nothing proves it, so treat it as a guess",
		},
		{
			"a retracted fact",
			View{ID: "f-abc234", Claim: "The API listens on port 8080", Status: fact.Holds, Check: "port localhost:8080",
				Detail: "localhost:8080 accepts connections", CheckedAt: ago(5 * day),
				Retracted: &fact.Retraction{At: start.Add(-3 * day), By: "alex", Reason: "the port moved to 9090"}},
			"RETRACTED f-abc234  The API listens on port 8080\n" +
				"          retracted 3d ago by alex: the port moved to 9090",
		},
		{
			"a fact retracted by nobody in particular",
			View{ID: "f-abc234", Claim: "Deploys happen on Mondays", Status: fact.Unproven,
				Retracted: &fact.Retraction{At: start.Add(-5 * time.Minute), Reason: "they do not"}},
			"RETRACTED f-abc234  Deploys happen on Mondays\n" +
				"          retracted 5m ago: they do not",
		},
		{
			"a replaced fact",
			View{ID: "f-abc234", Claim: "The API listens on port 8080", Status: fact.Broken, Check: "port localhost:8080",
				Detail: refusedDetail, CheckedAt: ago(time.Hour), ReplacedBy: "f-xyz789"},
			"REPLACED  f-abc234  The API listens on port 8080\n" +
				"          replaced by f-xyz789",
		},
		{
			"a fact both replaced and retracted",
			View{ID: "f-abc234", Claim: "The API listens on port 8080", Status: fact.Holds, ReplacedBy: "f-xyz789",
				Retracted: &fact.Retraction{At: start.Add(-time.Hour), By: "alex", Reason: "it was wrong all along"}},
			"RETRACTED f-abc234  The API listens on port 8080\n" +
				"          retracted 1h ago by alex: it was wrong all along",
		},
		{
			"one whose check did not finish this time",
			View{ID: "f-abc234", Claim: "The API listens on port 8080", Status: fact.Holds, Check: "port localhost:8080",
				Detail: "localhost:8080 accepts connections", CheckedAt: ago(4 * time.Minute), Late: true},
			"TIMED OUT f-abc234  The API listens on port 8080\n" +
				"          not proved just now: the check did not finish in time · it held when last checked 4m ago\n" +
				"          check: port localhost:8080",
		},
		{
			"a broken one whose check did not finish this time",
			View{ID: "f-abc234", Claim: "The API listens on port 8080", Status: fact.Broken, Check: "port localhost:8080",
				Detail: refusedDetail, CheckedAt: ago(4 * time.Minute), Held: ago(time.Hour), Late: true},
			"BROKEN    f-abc234  The API listens on port 8080\n" +
				"          proof failed: " + refusedDetail + " · last checked 4m ago; the check did not finish in time just now · it last held 1h ago\n" +
				"          check: port localhost:8080",
		},
		{
			"a check that has never run on this machine",
			View{ID: "f-abc234", Claim: "The API listens on port 8080", Status: fact.Blocked, Check: "port localhost:8080",
				Detail: "this check has not run on this machine yet"},
			"BLOCKED   f-abc234  The API listens on port 8080\n" +
				"          not checked: this check has not run on this machine yet\n" +
				"          check: port localhost:8080",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.v.Text(start)
			if got != tc.want {
				t.Errorf("text =\n%s\nwant\n%s", got, tc.want)
			}
			if strings.HasSuffix(got, "\n") {
				t.Error("the block ends with an empty line")
			}
		})
	}
}

func TestHowOldANoteIsInWords(t *testing.T) {
	tests := []struct {
		old  time.Duration
		want string
	}{
		{0, "under a minute"},
		{59 * time.Second, "under a minute"},
		{time.Minute, "1m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h"},
		{47*time.Hour + 59*time.Minute, "47h"},
		{48 * time.Hour, "2d"},
		{41 * day, "41d"},
		{-time.Hour, "under a minute"}, // a clock set back must not print nonsense
	}
	for _, tc := range tests {
		v := View{ID: "f-abc234", Claim: "A note", Status: fact.Unproven, Created: start.Add(-tc.old), NotedAt: ago(tc.old)}
		want := "UNPROVEN  f-abc234  A note\n          a note, " + tc.want + " old; it has no check, so nothing proves it"
		if got := v.Text(start); got != want {
			t.Errorf("a note %v old reads\n%s\nwant\n%s", tc.old, got, want)
		}
	}
}

func TestCountsInOneLine(t *testing.T) {
	tests := []struct {
		c    Counts
		want string
	}{
		{Counts{}, "no facts"},
		{Counts{Total: 1, Holds: 1}, "1 fact: 1 holds"},
		{Counts{Total: 1, Broken: 1}, "1 fact: 1 broken"},
		{Counts{Total: 2, Holds: 2}, "2 facts: 2 hold"},
		{Counts{Total: 2, Holds: 1, Unproven: 1}, "2 facts: 1 unproven, 1 holds"},
		{Counts{Total: 9, Holds: 6, Broken: 1, Unproven: 2}, "9 facts: 1 broken, 2 unproven, 6 hold"},
		// What needs attention is always said first, in the order a list uses.
		{Counts{Total: 15, Broken: 1, Blocked: 2, Stale: 3, Unproven: 4, Holds: 5}, "15 facts: 1 broken, 2 blocked, 3 stale, 4 unproven, 5 hold"},
	}
	for _, tc := range tests {
		if got := tc.c.String(); got != tc.want {
			t.Errorf("%+v reads %q, want %q", tc.c, got, tc.want)
		}
	}
}

// Three facts used by the texts below: one of each kind an agent must treat
// differently.
var (
	brokenView = View{ID: "f-aaaaaa", Claim: "The API listens on port 8080", Status: fact.Broken, Check: "port localhost:8080",
		Detail: refusedDetail, CheckedAt: ago(0), Held: ago(2 * time.Hour), Fresh: true}
	blockedView = View{ID: "f-bbbbbb", Claim: "The database accepts connections", Status: fact.Blocked, Check: "port db.internal:5432",
		Detail: blockedDetail, CheckedAt: ago(0), Fresh: true}
	staleView = View{ID: "f-cccccc", Claim: "The refresh token lasts 30 days", Status: fact.Stale, By: "claude",
		Created: start.Add(-41 * day), NotedAt: ago(41 * day), StaleAt: ago(11 * day)}
	noteView = View{ID: "f-dddddd", Claim: "Deploys happen on Fridays", Status: fact.Unproven,
		Created: start.Add(-6 * day), NotedAt: ago(6 * day), StaleAt: ago(-24 * day)}
	holdsView = View{ID: "f-eeeeee", Claim: "The project targets Go 1.24", Status: fact.Holds, Check: `contains go.mod "go 1.24"`,
		Detail: `go.mod contains "go 1.24" (line 3)`, CheckedAt: ago(0), Held: ago(0), Fresh: true}
)

const (
	brokenBlock = "BROKEN    f-aaaaaa  The API listens on port 8080\n" +
		"          proof failed: " + refusedDetail + " · checked just now · it last held 2h ago\n" +
		"          check: port localhost:8080"
	blockedBlock = "BLOCKED   f-bbbbbb  The database accepts connections\n" +
		"          not checked: " + blockedDetail + "\n" +
		"          check: port db.internal:5432"
	staleBlock = "STALE     f-cccccc  The refresh token lasts 30 days\n" +
		"          a note by claude, 41d old, past its 30d life; nothing proves it, so treat it as a guess"
	noteBlock = "UNPROVEN  f-dddddd  Deploys happen on Fridays\n" +
		"          a note, 6d old; it has no check, so nothing proves it"
	holdsBlock = "HOLDS     f-eeeeee  The project targets Go 1.24\n" +
		"          proof: go.mod contains \"go 1.24\" (line 3) · checked just now"

	brokenAdvice  = "BROKEN: this was true once and is not now. Do not rely on it. If you find out what is true now, remember that with replaces set to the old id."
	blockedAdvice = "BLOCKED: the check was not run, so this is neither proved nor disproved. Only a person can allow the check."
	notesAdvice   = "UNPROVEN and STALE: notes with no check. Confirm them yourself before relying on them."
)

func TestARecallAsText(t *testing.T) {
	const policyNote = "the policy file was changed and is not approved on this machine"
	tests := []struct {
		name string
		r    Recalled
		want string
	}{
		{"nothing stored at all", Recalled{}, "Nothing is remembered here yet."},
		{"nothing that matches, of several", Recalled{Known: 5},
			"Nothing remembered matches that. (5 facts are stored; recall with an empty query lists them.)"},
		{"nothing that matches, of one", Recalled{Known: 1},
			"Nothing remembered matches that. (1 fact is stored; recall with an empty query lists it.)"},
		{"one fact that holds needs no advice", Recalled{Facts: []View{holdsView}, Matched: 1, Known: 5},
			"1 fact: 1 holds.\n\n" + holdsBlock},
		{"one of each, with what each means for the work",
			Recalled{Facts: []View{brokenView, blockedView, staleView, noteView, holdsView}, Matched: 5, Known: 5},
			"5 facts: 1 broken, 1 blocked, 1 stale, 1 unproven, 1 holds.\n\n" +
				brokenBlock + "\n" + blockedBlock + "\n" + staleBlock + "\n" + noteBlock + "\n" + holdsBlock + "\n\n" +
				brokenAdvice + "\n" + blockedAdvice + "\n" + notesAdvice},
		{"only the advice that applies", Recalled{Facts: []View{noteView, holdsView}, Matched: 2, Known: 2},
			"2 facts: 1 unproven, 1 holds.\n\n" + noteBlock + "\n" + holdsBlock + "\n\n" + notesAdvice},
		{"a stale note alone gets the advice about notes", Recalled{Facts: []View{staleView}, Matched: 1, Known: 1},
			"1 fact: 1 stale.\n\n" + staleBlock + "\n\n" + notesAdvice},
		{"more matched than are shown", Recalled{Facts: []View{brokenView, holdsView}, Matched: 7, Known: 30},
			"2 facts: 1 broken, 1 holds (showing 2 of 7 that match).\n\n" + brokenBlock + "\n" + holdsBlock + "\n\n" + brokenAdvice},
		{"with a note about the policy last", Recalled{Facts: []View{blockedView}, Matched: 1, Known: 1, Note: policyNote},
			"1 fact: 1 blocked.\n\n" + blockedBlock + "\n\n" + blockedAdvice + "\n\n" + policyNote},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.Text(start); got != tc.want {
				t.Errorf("text =\n%s\n\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestAVerifyAsText(t *testing.T) {
	tests := []struct {
		name string
		v    Verified
		want string
	}{
		{"nothing to prove", Verified{}, "There are no facts with checks to prove."},
		{"everything holds", Verified{Facts: []View{holdsView}, Counts: Counts{Total: 1, Holds: 1}},
			"Proved again just now. 1 fact: 1 holds.\n\n" + holdsBlock},
		{"something is wrong",
			Verified{Facts: []View{brokenView, blockedView, holdsView}, Counts: Counts{Total: 3, Holds: 1, Broken: 1, Blocked: 1}, Note: "a note about the policy"},
			"Proved again just now. 3 facts: 1 broken, 1 blocked, 1 holds.\n\n" +
				brokenBlock + "\n" + blockedBlock + "\n" + holdsBlock + "\n\n" +
				brokenAdvice + "\n" + blockedAdvice + "\n\n" + "a note about the policy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.Text(start); got != tc.want {
				t.Errorf("text =\n%s\n\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestAListAsText(t *testing.T) {
	retracted := View{ID: "f-ffffff", Claim: "Deploys happen on Mondays", Status: fact.Unproven,
		Retracted: &fact.Retraction{At: start.Add(-time.Hour), By: "alex", Reason: "they do not"}}
	replaced := View{ID: "f-gggggg", Claim: "The API listens on port 3000", Status: fact.Broken, ReplacedBy: "f-aaaaaa"}
	const withdrawn = "Withdrawn:\n" +
		"RETRACTED f-ffffff  Deploys happen on Mondays\n" +
		"          retracted 1h ago by alex: they do not\n" +
		"REPLACED  f-gggggg  The API listens on port 3000\n" +
		"          replaced by f-aaaaaa"

	tests := []struct {
		name string
		l    Listed
		want string
	}{
		{"an empty store", Listed{Facts: []View{}}, "Nothing is remembered here yet."},
		{"what is live", Listed{Facts: []View{brokenView, holdsView}, Counts: Counts{Total: 2, Holds: 1, Broken: 1}},
			"2 facts: 1 broken, 1 holds.\n\n" + brokenBlock + "\n" + holdsBlock},
		{"with the withdrawn ones under their own heading",
			Listed{Facts: []View{holdsView}, Counts: Counts{Total: 1, Holds: 1}, Gone: []View{retracted, replaced}, Note: "a note about the policy"},
			"1 fact: 1 holds.\n\n" + holdsBlock + "\n\n" + withdrawn + "\n\n" + "a note about the policy"},
		{"when everything was withdrawn", Listed{Facts: []View{}, Gone: []View{retracted, replaced}},
			"no facts.\n\n" + withdrawn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.l.Text(start); got != tc.want {
				t.Errorf("text =\n%s\n\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestAListAsMarkdown(t *testing.T) {
	const head = "# What Holdfast knows\n\n"
	const tail = " Statuses are as of each fact's last check; run `holdfast check` to prove them again.\n"

	signed := noteView
	signed.ID, signed.Claim, signed.By = "f-hhhhhh", "The staging box is rebuilt on Sundays", "alex"

	tests := []struct {
		name string
		l    Listed
		want string
	}{
		{"an empty store", Listed{Facts: []View{}}, head + "no facts." + tail},
		{"only what holds, with no heading for what is not there",
			Listed{Facts: []View{holdsView}, Counts: Counts{Total: 1, Holds: 1}},
			head + "1 fact: 1 holds." + tail +
				"\n## Holds\n\n" +
				"- **The project targets Go 1.24** (`f-eeeeee`)  \n  proof: go.mod contains \"go 1.24\" (line 3)\n"},
		{"everything, what is wrong first",
			Listed{Facts: []View{brokenView, blockedView, staleView, noteView, signed, holdsView},
				Counts: Counts{Total: 6, Holds: 1, Broken: 1, Blocked: 1, Unproven: 2, Stale: 1}},
			head + "6 facts: 1 broken, 1 blocked, 1 stale, 2 unproven, 1 holds." + tail +
				"\n## No longer true\n\n" +
				"- **The API listens on port 8080** (`f-aaaaaa`)  \n  proof failed: " + refusedDetail + "\n" +
				"\n## Could not be checked\n\n" +
				"- **The database accepts connections** (`f-bbbbbb`)  \n  not checked: " + blockedDetail + "\n" +
				"\n## Notes nobody has proved\n\n" +
				"- **The refresh token lasts 30 days** (`f-cccccc`)  \n  a note by claude, 41d old, with no check\n" +
				"- **Deploys happen on Fridays** (`f-dddddd`)  \n  a note, 6d old, with no check\n" +
				"- **The staging box is rebuilt on Sundays** (`f-hhhhhh`)  \n  a note by alex, 6d old, with no check\n" +
				"\n## Holds\n\n" +
				"- **The project targets Go 1.24** (`f-eeeeee`)  \n  proof: go.mod contains \"go 1.24\" (line 3)\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.l.Markdown(start); got != tc.want {
				t.Errorf("markdown =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}

	// Withdrawn facts are history, not knowledge: the document leaves them out.
	gone := Listed{Facts: []View{holdsView}, Counts: Counts{Total: 1, Holds: 1},
		Gone: []View{{ID: "f-ffffff", Claim: "Deploys happen on Mondays", Retracted: &fact.Retraction{At: start, Reason: "no"}}}}
	if md := gone.Markdown(start); strings.Contains(md, "Mondays") {
		t.Errorf("the document shows a withdrawn fact:\n%s", md)
	}
}

// The texts of a real engine and the hand-built ones above must be the same
// thing: this ties them together once, end to end.
func TestTheTextOfARealRecall(t *testing.T) {
	b := newBench(t)
	api := b.remember("The API listens on port 8080", "port localhost:8080")
	mod := b.remember("The project targets Go 1.24", `contains go.mod "go 1.24"`)
	note := b.remember("Deploys happen on Fridays", "")
	b.later(2 * time.Hour)
	b.change(func(w *sim.World) { w.Ports["localhost:8080"] = false })

	got := b.recall("", 0).Text(b.clock.now())
	want := "3 facts: 1 broken, 1 unproven, 1 holds.\n\n" +
		"BROKEN    " + api.ID + "  The API listens on port 8080\n" +
		"          proof failed: " + refusedDetail + " · checked just now · it last held 2h ago\n" +
		"          check: port localhost:8080\n" +
		"UNPROVEN  " + note.ID + "  Deploys happen on Fridays\n" +
		"          a note by claude, 2h old; it has no check, so nothing proves it\n" +
		"HOLDS     " + mod.ID + "  The project targets Go 1.24\n" +
		"          proof: go.mod contains \"go 1.24\" (line 3) · checked just now\n\n" +
		brokenAdvice + "\n" + notesAdvice
	if got != want {
		t.Errorf("text =\n%s\n\nwant\n%s", got, want)
	}
}

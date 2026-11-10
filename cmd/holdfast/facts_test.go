package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/engine"
)

const (
	goClaim = "Go is pinned to 1.24"
	goCheck = `contains go.mod "go 1.24"`
	// pad is how far the lines under a fact are set in.
	pad = "          "
)

var (
	seconds = regexp.MustCompile(`\b\d+s ago\b`)
	colour  = regexp.MustCompile("\x1b\\[[0-9;]*m")
	factID  = regexp.MustCompile(`^f-[a-z0-9]{6}$`)
)

// calm reads "3s ago" as "just now", so that a slow machine cannot fail a
// test that compares whole lines.
func calm(s string) string { return seconds.ReplaceAllString(s, "just now") }

func provedText(v engine.View) string {
	return "HOLDS     " + v.ID + "  " + goClaim + "\n" +
		pad + `proof: go.mod contains "go 1.24" (line 3) · checked just now`
}

func brokenText(v engine.View) string {
	return "BROKEN    " + v.ID + "  " + goClaim + "\n" +
		pad + `proof failed: go.mod does not contain "go 1.24" · checked just now · it last held just now` + "\n" +
		pad + "check: " + goCheck
}

func noteText(v engine.View) string {
	return "UNPROVEN  " + v.ID + "  " + v.Claim + "\n" +
		pad + "a note by alex, under a minute old; it has no check, so nothing proves it"
}

// stock is a project that remembers one fact it has proved, one note, and
// one fact whose check the policy does not allow.
type stock struct {
	*desk
	proved, note, blocked engine.View
	// outside is a folder the policy does not allow; the blocked fact's
	// file is in it.
	outside string
}

func stocked(t *testing.T) *stock {
	t.Helper()
	d := project(t)
	outside := folder(t)
	d.write(filepath.Join(outside, "data.txt"), "hello\n")
	return &stock{
		desk:    d,
		outside: outside,
		proved:  d.add(goClaim, goCheck, "--tag", "build"),
		note:    d.add("Deploys go out on Fridays", "", "--tag", "ops"),
		blocked: d.add("The data is there", "file "+filepath.Join(outside, "data.txt"), "--tag", "build"),
	}
}

func (s *stock) blockedText() string {
	file := filepath.Join(s.outside, "data.txt")
	return "BLOCKED   " + s.blocked.ID + "  The data is there\n" +
		pad + "not checked: " + file + " is outside the folders the policy allows. A person can allow it: holdfast allow files " + s.outside + "\n" +
		pad + "check: file " + file
}

// breakGo makes the proved fact untrue.
func (d *desk) breakGo() { d.write("go.mod", "module example.com/atlas\n\ngo 1.25\n") }

const (
	brokenAdvice  = "BROKEN: this was true once and is not now. Do not rely on it. If you find out what is true now, remember that with replaces set to the old id."
	blockedAdvice = "BLOCKED: the check was not run, so this is neither proved nor disproved. Only a person can allow the check."
)

// --- add ---

func TestAddStoresAFactItHasJustProved(t *testing.T) {
	d := project(t)
	got := d.ok("add", goClaim, "--check", goCheck)
	l := d.listed()
	if l.Counts.Total != 1 || l.Counts.Holds != 1 {
		t.Fatalf("after one add the store holds %+v", l.Counts)
	}
	v := l.Facts[0]
	if want := "Stored, and proved just now.\n" + provedText(v) + "\n"; calm(got.out) != want {
		t.Errorf("add said:\n%s\nwant:\n%s", got.out, want)
	}
	if !factID.MatchString(v.ID) {
		t.Errorf("the id is %q, want f- and six letters or digits", v.ID)
	}
	if v.Claim != goClaim || v.Check != goCheck || v.Kind != "contains" || v.By != "alex" {
		t.Errorf("stored as %+v", v)
	}
	if v.Held == nil || v.CheckedAt == nil || len(v.History) != 1 || time.Since(v.Created) > time.Minute {
		t.Errorf("the proof is not kept with the fact: %+v", v)
	}
}

func TestAddRefusesAFactWhoseCheckFails(t *testing.T) {
	d := project(t)
	facts := filepath.Join(d.cwd, ".holdfast", "facts.jsonl")
	before := read(t, facts)
	for name, c := range map[string]struct {
		check string
		why   string
	}{
		"a file that is not there": {"file missing.txt", "the check fails right now, so this is not a fact yet: missing.txt does not exist"},
		"text the file lacks":      {`contains go.mod "go 1.99"`, "the check fails right now, so this is not a fact yet: go.mod does not contain"},
		"a thing that is there":    {"not file go.mod", "the check fails right now, so this is not a fact yet: go.mod exists"},
	} {
		t.Run(name, func(t *testing.T) {
			got := d.run("add", "Something that is not so", "--check", c.check)
			if got.code != 1 || got.out != "" {
				t.Errorf("exit %d, printed %q; want exit 1 and nothing stored", got.code, got.out)
			}
			if !strings.HasPrefix(got.err, "Not stored: "+c.why) {
				t.Errorf("the refusal reads: %q", got.err)
			}
		})
	}
	// The refusal says what to do next.
	got := d.run("add", "The notes are missing", "--check", "file missing.txt")
	says(t, "the refusal", got.err, "Correct the claim or the check", `start the check with "not"`)
	if after := read(t, facts); after != before {
		t.Errorf("a refused fact was written to the journal:\n%s", after)
	}
	// Following that advice works.
	if v := d.add("There are no notes yet", "not file missing.txt"); v.Status != "holds" {
		t.Errorf("the fact that something is not so is %s: %s", v.Status, v.Detail)
	}
}

func TestAddRefusesWhatItCannotRead(t *testing.T) {
	d := project(t)
	for name, c := range map[string]struct {
		args []string
		why  string
	}{
		"a check in no known form":            {[]string{"add", "It is true", "--check", "is true"}, `Not stored: unknown check "is". A check starts with one of: `},
		"a time to live it cannot read":       {[]string{"add", "A note", "--ttl", "soon"}, `Not stored: duration "soon": the unit must be s, m, h, d or w`},
		"an empty claim":                      {[]string{"add", ""}, "Not stored: the claim is empty: say the fact in one plain sentence"},
		"a claim of spaces":                   {[]string{"add", "   ", "--check", "file go.mod"}, "Not stored: the claim is empty"},
		"a fact to replace that is not there": {[]string{"add", "A note", "--replaces", "f-zzzzzz"}, "Not stored: f-zzzzzz cannot be replaced: there is no fact with that id"},
	} {
		t.Run(name, func(t *testing.T) {
			got := d.run(c.args...)
			if got.code != 1 || got.out != "" || !strings.HasPrefix(got.err, c.why) {
				t.Errorf("exit %d, %q, %q; want exit 1 and %q", got.code, got.out, got.err, c.why)
			}
			lacks(t, "the refusal", got.err, "Usage:")
		})
	}
	if n := d.listed().Counts.Total; n != 0 {
		t.Errorf("%d facts were stored by commands that were refused", n)
	}
}

func TestAddWithoutACheckStoresANote(t *testing.T) {
	d := project(t)
	got := d.ok("add", "Deploys go out on Fridays")
	l := d.listed()
	if l.Counts.Total != 1 || l.Counts.Unproven != 1 {
		t.Fatalf("after adding a note the store holds %+v", l.Counts)
	}
	v := l.Facts[0]
	if want := "Stored as a note. It has no check, so it will go stale.\n" + noteText(v) + "\n"; got.out != want {
		t.Errorf("add said:\n%s\nwant:\n%s", got.out, want)
	}
	if v.Check != "" || v.StaleAt == nil || v.NotedAt == nil {
		t.Fatalf("the note is stored as %+v", v)
	}
	// Thirty days, unless told otherwise.
	if life := v.StaleAt.Sub(*v.NotedAt); life != 30*24*time.Hour {
		t.Errorf("a note is believed for %v, want 30 days", life)
	}
}

func TestAddStoresAFactThePolicyWillNotLetItProve(t *testing.T) {
	d := project(t)
	outside := folder(t)
	file := filepath.Join(outside, "data.txt")
	d.write(file, "hello\n")
	got := d.ok("add", "The data is there", "--check", "file "+file)
	l := d.listed()
	if l.Counts.Total != 1 || l.Counts.Blocked != 1 {
		t.Fatalf("the store holds %+v, want one blocked fact", l.Counts)
	}
	s := &stock{desk: d, outside: outside, blocked: l.Facts[0]}
	if want := "Stored, but not proved: its check was not run.\n" + s.blockedText() + "\n"; got.out != want {
		t.Errorf("add said:\n%s\nwant:\n%s", got.out, want)
	}
	// Not proved and not disproved: it never held, and the file was not read.
	if v := l.Facts[0]; v.Held != nil || v.Status != "blocked" {
		t.Errorf("the blocked fact is stored as %+v", v)
	}
}

func TestAddingTheSameFactAgainStoresNothingNew(t *testing.T) {
	d := project(t)
	first := d.add(goClaim, goCheck)
	got := d.ok("add", goClaim, "--check", goCheck)
	if want := "Already remembered.\n" + provedText(first) + "\n"; calm(got.out) != want {
		t.Errorf("the second add said:\n%s\nwant:\n%s", got.out, want)
	}
	var again struct {
		Fact    engine.View `json:"fact"`
		Already bool        `json:"already"`
	}
	decode(t, d.ok("add", goClaim, "--check", goCheck, "--json").out, &again)
	if !again.Already || again.Fact.ID != first.ID {
		t.Errorf("the third add printed already=%v and the id %s, want true and %s", again.Already, again.Fact.ID, first.ID)
	}
	if n := d.listed().Counts.Total; n != 1 {
		t.Errorf("the store holds %d facts, want the one", n)
	}
}

func TestAddTakesExactlyOneClaim(t *testing.T) {
	d := project(t)
	for _, args := range [][]string{
		{"add"},
		{"add", "--check", "file go.mod"},
		{"add", "The API listens", "on port 8080"},
		{"add", "Go", "is", "pinned", "--check", goCheck},
	} {
		got := d.run(args...)
		if got.code != 2 || got.out != "" {
			t.Errorf("holdfast %v: exit %d, printed %q; want exit 2", args, got.code, got.out)
		}
		says(t, "the complaint", got.err, "holdfast: give the claim as one quoted sentence\n", "Usage: holdfast add ")
	}
	if n := d.listed().Counts.Total; n != 0 {
		t.Errorf("%d facts were stored from commands that were not understood", n)
	}
}

func TestFlagsMayStandBeforeOrAfterTheClaim(t *testing.T) {
	d := project(t)
	for name, args := range map[string][]string{
		"after":        {"add", "The module file exists", "--check", "file go.mod", "--tag", "build"},
		"before":       {"add", "--check", "file go.mod", "--tag", "build", "The module file exists"},
		"around":       {"add", "--tag", "build", "The module file exists", "--check", "file go.mod"},
		"with equals":  {"add", "--check=file go.mod", "The module file exists", "--tag=build"},
		"one dash":     {"add", "The module file exists", "-check", "file go.mod", "-tag", "build"},
		"another name": {"remember", "The module file exists", "--check", "file go.mod", "--tag", "build"},
	} {
		t.Run(name, func(t *testing.T) {
			d := project(t)
			d.ok(args...)
			l := d.listed()
			if l.Counts.Total != 1 {
				t.Fatalf("the store holds %+v", l.Counts)
			}
			v := l.Facts[0]
			if v.Claim != "The module file exists" || v.Check != "file go.mod" || strings.Join(v.Tags, ",") != "build" || v.Status != "holds" {
				t.Errorf("stored as %+v", v)
			}
		})
	}
	// Two dashes alone end the flags: what follows is the claim, word for word.
	d.ok("add", "--tag", "odd", "--", "--check is a flag of add")
	if l := d.listed(); l.Counts.Total != 1 || l.Facts[0].Claim != "--check is a flag of add" || l.Facts[0].Check != "" {
		t.Errorf("after two dashes the claim was stored as %+v", l.Facts)
	}
}

func TestAFactIsStoredUnderTheNameOfWhoAddedIt(t *testing.T) {
	for name, c := range map[string]struct {
		env  map[string]string
		as   string
		want string
	}{
		"the name given":          {map[string]string{"HOLDFAST_AGENT": "claude", "USER": "alex", "LOGNAME": "root"}, "sara", "sara"},
		"the agent's name":        {map[string]string{"HOLDFAST_AGENT": "claude", "USER": "alex", "LOGNAME": "root"}, "", "claude"},
		"the user":                {map[string]string{"USER": "alex", "LOGNAME": "root"}, "", "alex"},
		"the login name":          {map[string]string{"LOGNAME": "root"}, "", "root"},
		"nobody: no name at all":  {map[string]string{}, "", ""},
		"an empty name is no one": {map[string]string{"HOLDFAST_AGENT": "", "USER": "alex"}, "", "alex"},
	} {
		t.Run(name, func(t *testing.T) {
			d := project(t)
			d.env = c.env
			var more []string
			if c.as != "" {
				more = []string{"--as", c.as}
			}
			if v := d.add("Deploys go out on Fridays", "", more...); v.By != c.want {
				t.Errorf("stored by %q, want %q", v.By, c.want)
			}
		})
	}
}

func TestAddKeepsTagsLifeAndWhatItReplaces(t *testing.T) {
	d := project(t)
	d.write("VERSION", "1.0\n")

	// Tags are lower-case words; a leading # is how people write them.
	tagged := d.add(goClaim, goCheck, "--tag", "Build", "--tag", "#Go")
	if got := strings.Join(tagged.Tags, ","); got != "build,go" {
		t.Errorf("the tags are %q, want build,go", got)
	}

	// A note is believed for as long as it was given.
	short := d.add("The freeze ends on Monday", "", "--ttl", "2d")
	if short.StaleAt == nil || short.NotedAt == nil || short.StaleAt.Sub(*short.NotedAt) != 48*time.Hour {
		t.Errorf("a note with --ttl 2d is stored as %+v", short)
	}

	// A fact that takes the place of another puts the old one aside.
	old := d.add("The version file exists", "file VERSION")
	got := d.ok("add", "The version file is there", "--check", "file VERSION", "--replaces", old.ID)
	says(t, "add --replaces", got.out, "Stored, and proved just now.\nHOLDS     ")
	l := d.listed()
	if l.Counts.Total != 3 || len(l.Gone) != 1 {
		t.Fatalf("after a replacement there are %d live facts and %d put aside, want 3 and 1", l.Counts.Total, len(l.Gone))
	}
	var fresh engine.View
	for _, v := range l.Facts {
		if v.ID == old.ID {
			t.Errorf("the replaced fact %s is still listed as live", old.ID)
		}
		if v.Claim == "The version file is there" {
			fresh = v
		}
	}
	if fresh.Replaces != old.ID || l.Gone[0].ID != old.ID || l.Gone[0].ReplacedBy != fresh.ID {
		t.Errorf("the new fact replaces %q, and the old one is replaced by %q", fresh.Replaces, l.Gone[0].ReplacedBy)
	}
	want := "REPLACED  " + old.ID + "  The version file exists\n" + pad + "replaced by " + fresh.ID
	says(t, "list --all", d.ok("list", "--all").out, "\nWithdrawn:\n"+want+"\n")
	says(t, "why, of the new fact", d.ok("why", fresh.ID).out, "\n"+pad+"replaces "+old.ID)
}

func TestANoteGoesStaleWhenItsLifeIsOver(t *testing.T) {
	d := project(t)
	v := d.add("The build is frozen", "", "--ttl", "1s")
	if v.Status != "unproven" || v.StaleAt == nil {
		t.Fatalf("the note is stored as %+v", v)
	}
	time.Sleep(time.Until(*v.StaleAt) + 30*time.Millisecond)
	want := "STALE     " + v.ID + "  The build is frozen\n" + pad + "a note by alex, under a minute old, past its 1s life; nothing proves it, so treat it as a guess\n"
	if got := d.ok("list", "--status", "stale"); got.out != "1 of 1 facts.\n\n"+want {
		t.Errorf("the stale note lists as:\n%s\nwant:\n%s", got.out, want)
	}
	if l := d.listed(); l.Counts.Stale != 1 || l.Counts.Unproven != 0 {
		t.Errorf("the counts are %+v, want one stale fact", l.Counts)
	}
	// Somebody saying it is still so starts its life again.
	got := d.ok("vouch", v.ID, "--as", "sara")
	says(t, "vouch", got.out, "Vouched for. Its time to live starts again from now.\nUNPROVEN  "+v.ID, "confirmed by sara")
	if l := d.listed(); l.Counts.Stale != 0 || l.Counts.Unproven != 1 {
		t.Errorf("after vouching the counts are %+v, want one unproven fact", l.Counts)
	}
}

// --- list ---

func TestListShowsEveryFactWorstNewsFirst(t *testing.T) {
	d := project(t)
	if got := d.ok("list"); got.out != "Nothing is remembered here yet.\n" {
		t.Errorf("an empty store lists as %q", got.out)
	}
	s := stocked(t)
	got := s.ok("list")
	want := "3 facts: 1 blocked, 1 unproven, 1 holds.\n\n" +
		s.blockedText() + "\n" + noteText(s.note) + "\n" + provedText(s.proved) + "\n"
	if calm(got.out) != want {
		t.Errorf("list said:\n%s\nwant:\n%s", got.out, want)
	}
	// A broken fact is the first thing to know.
	s.breakGo()
	s.run("check")
	got = s.ok("ls")
	want = "3 facts: 1 broken, 1 blocked, 1 unproven.\n\n" +
		brokenText(s.proved) + "\n" + s.blockedText() + "\n" + noteText(s.note) + "\n"
	if calm(got.out) != want {
		t.Errorf("list said:\n%s\nwant:\n%s", got.out, want)
	}
}

// Listing reads what is known; it is check and recall that prove.
func TestListShowsTheLastResultWithoutProvingAgain(t *testing.T) {
	s := stocked(t)
	s.breakGo()
	if got := s.ok("list"); !strings.Contains(got.out, "HOLDS     "+s.proved.ID) {
		t.Errorf("list ran the check again:\n%s", got.out)
	}
	if got := s.run("check"); got.code != 1 {
		t.Fatalf("check: exit %d\n%s%s", got.code, got.out, got.err)
	}
	if got := s.ok("list"); !strings.Contains(got.out, "BROKEN    "+s.proved.ID) {
		t.Errorf("list does not show what check found:\n%s", got.out)
	}
}

func TestListNarrowsByStatusAndTag(t *testing.T) {
	s := stocked(t)
	for name, c := range map[string]struct {
		args []string
		want []string
	}{
		"a status":                 {[]string{"--status", "blocked"}, []string{s.blocked.ID}},
		"a status in capitals":     {[]string{"--status", "HOLDS"}, []string{s.proved.ID}},
		"a status nothing has":     {[]string{"--status", "broken"}, nil},
		"a tag":                    {[]string{"--tag", "build"}, []string{s.blocked.ID, s.proved.ID}},
		"a tag as people write it": {[]string{"--tag", "#Ops"}, []string{s.note.ID}},
		"a tag nothing has":        {[]string{"--tag", "nothing"}, nil},
		"both":                     {[]string{"--status", "holds", "--tag", "build"}, []string{s.proved.ID}},
		"both, matching nothing":   {[]string{"--status", "unproven", "--tag", "build"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			var l engine.Listed
			decode(t, s.ok(append([]string{"list", "--json"}, c.args...)...).out, &l)
			var ids []string
			for _, v := range l.Facts {
				ids = append(ids, v.ID)
			}
			if strings.Join(ids, " ") != strings.Join(c.want, " ") {
				t.Errorf("listed %v, want %v", ids, c.want)
			}
			// The counts are of the whole store, so the reader knows what was left out.
			if l.Counts.Total != 3 {
				t.Errorf("the counts are %+v, want the whole store's", l.Counts)
			}
			plain := s.ok(append([]string{"list"}, c.args...)...).out
			head := map[int]string{0: "0 of 3 facts.\n", 1: "1 of 3 facts.\n\n", 2: "2 of 3 facts.\n\n"}[len(c.want)]
			if !strings.HasPrefix(plain, head) {
				t.Errorf("list %v begins %q, want %q", c.args, plain, head)
			}
			for _, id := range []string{s.proved.ID, s.note.ID, s.blocked.ID} {
				if strings.Contains(plain, id) != has(c.want, id) {
					t.Errorf("list %v: is %s shown? %v, want %v", c.args, id, strings.Contains(plain, id), has(c.want, id))
				}
			}
		})
	}
	if got := s.ok("list", "--status", "blocked"); got.out != "1 of 3 facts.\n\n"+s.blockedText()+"\n" {
		t.Errorf("list --status blocked said:\n%s", got.out)
	}
}

func TestListRefusesAStatusNoFactCanHave(t *testing.T) {
	s := stocked(t)
	for _, word := range []string{"good", "hold", "retracted", "all", "holds,broken"} {
		for _, more := range [][]string{nil, {"--json"}} {
			got := s.run(append([]string{"list", "--status", word}, more...)...)
			if got.code != 2 || got.out != "" {
				t.Errorf("list --status %s %v: exit %d, printed %q; want exit 2", word, more, got.code, got.out)
			}
			says(t, "the complaint", got.err, `"`+word+`" is not a status; write holds, broken, blocked, unproven or stale`, "Usage: holdfast list")
		}
	}
}

func TestListTakesNoArguments(t *testing.T) {
	s := stocked(t)
	got := s.run("list", "go")
	if got.code != 2 || got.out != "" {
		t.Errorf("list go: exit %d, printed %q", got.code, got.out)
	}
	says(t, "the complaint", got.err, "holdfast: list takes no arguments; to search, use: holdfast recall \"<question>\"\n", "Usage: holdfast list")
}

func TestListAllAlsoShowsWhatWasWithdrawn(t *testing.T) {
	s := stocked(t)
	s.ok("retract", s.note.ID, "--reason", "we deploy every day now")
	withdrawn := "RETRACTED " + s.note.ID + "  Deploys go out on Fridays\n" + pad + "retracted just now by alex: we deploy every day now"

	plain := s.ok("list")
	if calm(plain.out) != "2 facts: 1 blocked, 1 holds.\n\n"+s.blockedText()+"\n"+provedText(s.proved)+"\n" {
		t.Errorf("list still shows a withdrawn fact, or lost another:\n%s", plain.out)
	}
	all := s.ok("list", "--all")
	if want := calm(plain.out) + "\nWithdrawn:\n" + withdrawn + "\n"; calm(all.out) != want {
		t.Errorf("list --all said:\n%s\nwant:\n%s", all.out, want)
	}

	// With everything withdrawn there is nothing live to list, and --all
	// still says what went.
	s.ok("retract", s.proved.ID, "--reason", "moved to 1.25")
	s.ok("retract", s.blocked.ID, "--reason", "the data moved")
	if got := s.ok("list"); got.out != "Nothing is remembered here yet.\n" {
		t.Errorf("a store with nothing live lists as:\n%s", got.out)
	}
	got := s.ok("list", "--all")
	if !strings.HasPrefix(got.out, "no facts.\n\nWithdrawn:\n") {
		t.Errorf("list --all with nothing live begins:\n%s", got.out)
	}
	says(t, "list --all", calm(got.out), withdrawn, "RETRACTED "+s.proved.ID, "RETRACTED "+s.blocked.ID)
}

func TestListJSONIsTheWholeListInOneValue(t *testing.T) {
	d := project(t)
	var empty map[string]json.RawMessage
	decode(t, d.ok("list", "--json").out, &empty)
	if string(empty["facts"]) != "[]" {
		t.Errorf(`an empty store has "facts": %s, want an empty list a program can loop over`, empty["facts"])
	}
	if _, there := empty["gone"]; there {
		t.Error(`list --json has "gone" without --all`)
	}

	s := stocked(t)
	s.ok("retract", s.note.ID, "--reason", "we deploy every day now")
	var l engine.Listed
	decode(t, s.ok("list", "--json").out, &l)
	if len(l.Facts) != 2 || l.Counts.Total != 2 || l.Counts.Holds != 1 || l.Counts.Blocked != 1 || len(l.Gone) != 0 {
		t.Errorf("list --json: %d facts, counts %+v, %d gone", len(l.Facts), l.Counts, len(l.Gone))
	}
	decode(t, s.ok("list", "--json", "--all").out, &l)
	if len(l.Facts) != 2 || len(l.Gone) != 1 || l.Gone[0].ID != s.note.ID {
		t.Fatalf("list --json --all: %d facts, gone %+v", len(l.Facts), l.Gone)
	}
	if r := l.Gone[0].Retracted; r == nil || r.Reason != "we deploy every day now" || r.By != "alex" {
		t.Errorf("the withdrawn fact carries %+v", r)
	}
	// The narrowed list is the same shape.
	var narrow map[string]json.RawMessage
	decode(t, s.ok("list", "--json", "--status", "stale").out, &narrow)
	if string(narrow["facts"]) != "[]" || narrow["counts"] == nil {
		t.Errorf("a narrowed list with nothing in it is %v", narrow)
	}
}

// --- check ---

func TestCheckProvesEveryFactAgainAndSaysHowItStands(t *testing.T) {
	d := project(t)
	if got := d.ok("check"); got.out != "There are no facts with checks to prove.\n" {
		t.Errorf("check on an empty store said %q", got.out)
	}

	s := stocked(t)
	got := s.ok("check")
	// The note has no check, so there is nothing about it to prove.
	want := "Proved again just now. 2 facts: 1 blocked, 1 holds.\n\n" +
		s.blockedText() + "\n" + provedText(s.proved) + "\n\n" + blockedAdvice + "\n"
	if calm(got.out) != want {
		t.Errorf("check said:\n%s\nwant:\n%s", got.out, want)
	}
	// Another name for the same thing.
	if again := s.ok("verify"); calm(again.out) != want {
		t.Errorf("verify said:\n%s", again.out)
	}
}

func TestCheckExitsOneWhenAFactNoLongerHolds(t *testing.T) {
	d := project(t)
	v := d.add(goClaim, goCheck)
	if got := d.ok("check"); calm(got.out) != "Proved again just now. 1 fact: 1 holds.\n\n"+provedText(v)+"\n" {
		t.Errorf("check said:\n%s", got.out)
	}

	d.breakGo()
	got := d.run("check")
	want := "Proved again just now. 1 fact: 1 broken.\n\n" + brokenText(v) + "\n\n" + brokenAdvice + "\n"
	if got.code != 1 || got.err != "" || calm(got.out) != want {
		t.Errorf("check after the file changed: exit %d, %q\n%s\nwant exit 1 and:\n%s", got.code, got.err, got.out, want)
	}
	// It stays broken, and stays exit 1, until the world is put right.
	if got := d.run("check"); got.code != 1 {
		t.Errorf("a second check of a broken fact: exit %d", got.code)
	}
	if now := d.why(v.ID); now.Status != "broken" || now.Held == nil || len(now.History) != 2 {
		t.Errorf("the broken fact is %s, last held %v, with %d changes of state", now.Status, now.Held, len(now.History))
	}

	d.write("go.mod", goMod)
	if got := d.ok("check"); !strings.Contains(got.out, "1 fact: 1 holds.") {
		t.Errorf("check after the file was put back said:\n%s", got.out)
	}
	if now := d.why(v.ID); now.Status != "holds" || len(now.History) != 3 {
		t.Errorf("the mended fact is %s with %d changes of state, want holds and 3", now.Status, len(now.History))
	}
}

func TestCheckStrictAlsoFailsOnAFactThatCouldNotBeChecked(t *testing.T) {
	s := stocked(t)
	if got := s.run("check"); got.code != 0 {
		t.Errorf("check with a blocked fact: exit %d; blocked is not broken", got.code)
	}
	got := s.run("check", "--strict")
	if got.code != 1 || got.err != "" {
		t.Errorf("check --strict with a blocked fact: exit %d, %q; want exit 1", got.code, got.err)
	}
	says(t, "check --strict", got.out, "BLOCKED   "+s.blocked.ID, blockedAdvice)

	// With the blocked fact gone, strict has nothing to object to.
	s.ok("retract", s.blocked.ID, "--reason", "the data moved")
	s.ok("check", "--strict")
	// And a broken fact fails with or without it.
	s.breakGo()
	for _, args := range [][]string{{"check"}, {"check", "--strict"}} {
		if got := s.run(args...); got.code != 1 {
			t.Errorf("holdfast %v with a broken fact: exit %d", args, got.code)
		}
	}
}

func TestCheckQuietPrintsOnlyWhatDoesNotHold(t *testing.T) {
	s := stocked(t)
	got := s.ok("check", "--quiet")
	if want := "2 facts: 1 blocked, 1 holds.\n\n" + s.blockedText() + "\n"; got.out != want {
		t.Errorf("check --quiet said:\n%s\nwant:\n%s", got.out, want)
	}
	s.breakGo()
	got = s.run("check", "--quiet")
	want := "2 facts: 1 broken, 1 blocked.\n\n" + brokenText(s.proved) + "\n" + s.blockedText() + "\n"
	if got.code != 1 || calm(got.out) != want {
		t.Errorf("check --quiet with a broken fact: exit %d\n%s\nwant:\n%s", got.code, got.out, want)
	}

	// When everything holds there is one line.
	d := project(t)
	d.add(goClaim, goCheck)
	if got := d.ok("check", "--quiet"); got.out != "1 fact: 1 holds.\n" {
		t.Errorf("check --quiet with nothing wrong said %q", got.out)
	}
}

func TestCheckByIdProvesOnlyTheFactsNamed(t *testing.T) {
	s := stocked(t)
	s.breakGo()
	// The broken fact was not named, so it is not checked and not counted.
	got := s.ok("check", s.blocked.ID)
	if want := "Proved again just now. 1 fact: 1 blocked.\n\n" + s.blockedText() + "\n\n" + blockedAdvice + "\n"; got.out != want {
		t.Errorf("check of one fact said:\n%s\nwant:\n%s", got.out, want)
	}
	if v := s.why(s.proved.ID); v.Status != "holds" {
		t.Errorf("a fact that was not named was checked: it is now %s", v.Status)
	}
	if got := s.run("check", s.blocked.ID, s.proved.ID); got.code != 1 || !strings.Contains(got.out, "2 facts: 1 broken, 1 blocked.") {
		t.Errorf("check of two facts: exit %d\n%s", got.code, got.out)
	}

	for _, id := range []string{"f-zzzzzz", "nonsense"} {
		got := s.run("check", s.blocked.ID, id)
		if want := `holdfast: there is no fact with the id "` + id + `"` + "\n"; got.code != 1 || got.out != "" || got.err != want {
			t.Errorf("check of %s: exit %d, %q, %q; want exit 1 and %q", id, got.code, got.out, got.err, want)
		}
	}
}

func TestCheckJSONCarriesTheSameAnswerAndExitCode(t *testing.T) {
	d := project(t)
	var empty map[string]json.RawMessage
	decode(t, d.ok("check", "--json").out, &empty)
	if string(empty["facts"]) != "[]" {
		t.Errorf(`check --json on an empty store has "facts": %s, want an empty list`, empty["facts"])
	}

	s := stocked(t)
	var v engine.Verified
	decode(t, s.ok("check", "--json").out, &v)
	if len(v.Facts) != 2 || v.Counts.Total != 2 || v.Counts.Holds != 1 || v.Counts.Blocked != 1 {
		t.Errorf("check --json: %d facts, counts %+v", len(v.Facts), v.Counts)
	}
	for _, f := range v.Facts {
		if f.ID == s.proved.ID && !f.Fresh {
			t.Error("the fact that was just proved is not marked as fresh")
		}
	}
	s.breakGo()
	got := s.run("check", "--json")
	if got.code != 1 || got.err != "" {
		t.Errorf("check --json with a broken fact: exit %d, %q; want exit 1", got.code, got.err)
	}
	decode(t, got.out, &v)
	if v.Counts.Broken != 1 || v.Facts[0].ID != s.proved.ID || v.Facts[0].Status != "broken" {
		t.Errorf("check --json: counts %+v, first %s %s", v.Counts, v.Facts[0].ID, v.Facts[0].Status)
	}
	if got := s.run("check", "--json", "--strict", s.blocked.ID); got.code != 1 {
		t.Errorf("check --json --strict of a blocked fact: exit %d", got.code)
	}
}

// --- recall ---

func TestRecallFindsWhatBearsOnTheQuestionAndProvesItFirst(t *testing.T) {
	s := stocked(t)
	got := s.ok("recall", "pinned")
	if want := "1 fact: 1 holds.\n\n" + provedText(s.proved) + "\n"; calm(got.out) != want {
		t.Errorf("recall said:\n%s\nwant:\n%s", got.out, want)
	}
	// The words of a question need no quoting.
	if got := s.ok("recall", "which", "go", "is", "pinned"); !strings.Contains(got.out, s.proved.ID) {
		t.Errorf("recall with several words did not find the fact:\n%s", got.out)
	}

	// What it returns was checked a moment ago, not when it was stored.
	s.breakGo()
	got = s.ok("recall", "pinned")
	want := "1 fact: 1 broken.\n\n" + brokenText(s.proved) + "\n\n" + brokenAdvice + "\n"
	if calm(got.out) != want {
		t.Errorf("recall after the file changed said:\n%s\nwant:\n%s", got.out, want)
	}
	if v := s.why(s.proved.ID); v.Status != "broken" {
		t.Errorf("what recall found out was not kept: the fact is %s", v.Status)
	}
}

func TestRecallWithNoWordsListsEverything(t *testing.T) {
	d := project(t)
	if got := d.ok("recall"); got.out != "Nothing is remembered here yet.\n" {
		t.Errorf("recall on an empty store said %q", got.out)
	}
	s := stocked(t)
	got := s.ok("recall")
	says(t, "recall", got.out, "3 facts: 1 blocked, 1 unproven, 1 holds.", s.proved.ID, s.note.ID, s.blocked.ID, blockedAdvice)
}

func TestRecallSaysSoWhenNothingMatches(t *testing.T) {
	s := stocked(t)
	got := s.ok("recall", "kubernetes")
	if want := "Nothing remembered matches that. (3 facts are stored; recall with an empty query lists them.)\n"; got.out != want {
		t.Errorf("recall said %q, want %q", got.out, want)
	}
	d := project(t)
	d.add(goClaim, goCheck)
	if got := d.ok("recall", "kubernetes"); got.out != "Nothing remembered matches that. (1 fact is stored; recall with an empty query lists it.)\n" {
		t.Errorf("recall said %q", got.out)
	}
}

func TestRecallReturnsAtMostAsManyAsAsked(t *testing.T) {
	d := project(t)
	d.write("a.txt", "x\n")
	d.write("b.txt", "x\n")
	d.write("c.txt", "x\n")
	for _, name := range []string{"a", "b", "c"} {
		d.add("The fixture "+name+" is in place", "file "+name+".txt")
	}
	for name, c := range map[string]struct {
		args []string
		want int
	}{
		"one":                  {[]string{"-n", "1", "fixture"}, 1},
		"two, flag last":       {[]string{"fixture", "-n", "2"}, 2},
		"more than there are":  {[]string{"-n", "50", "fixture"}, 3},
		"no limit named":       {[]string{"fixture"}, 3},
		"zero means the usual": {[]string{"-n", "0", "fixture"}, 3},
	} {
		t.Run(name, func(t *testing.T) {
			var r engine.Recalled
			decode(t, d.ok(append([]string{"recall", "--json"}, c.args...)...).out, &r)
			if len(r.Facts) != c.want || r.Matched != 3 || r.Known != 3 {
				t.Errorf("%d facts, %d matched, %d known; want %d, 3, 3", len(r.Facts), r.Matched, r.Known, c.want)
			}
		})
	}
	var none map[string]json.RawMessage
	decode(t, d.ok("recall", "--json", "kubernetes").out, &none)
	if string(none["facts"]) != "[]" || string(none["matched"]) != "0" || string(none["known"]) != "3" {
		t.Errorf("recall --json with nothing found is %v", none)
	}
}

// --- why ---

func TestWhyShowsWhereAFactCameFromAndWhatItHasBeenThrough(t *testing.T) {
	s := stocked(t)

	got := s.ok("why", s.note.ID)
	if want := noteText(s.note) + "\n" + pad + "stored just now by alex · tags: ops\n"; calm(got.out) != want {
		t.Errorf("why, of a note, said:\n%s\nwant:\n%s", got.out, want)
	}

	// A fact with a check shows the check, once, and each change of state.
	got = s.ok("show", s.proved.ID)
	want := provedText(s.proved) + "\n" + pad + "check: " + goCheck + "\n" + pad + "stored just now by alex · tags: build\n\nHistory\n"
	if !strings.HasPrefix(calm(got.out), want) {
		t.Errorf("why said:\n%s\nwant it to begin:\n%s", got.out, want)
	}
	s.breakGo()
	s.run("check")
	got = s.ok("why", s.proved.ID)
	if n := strings.Count(got.out, pad+"check: "); n != 1 {
		t.Errorf("the check is shown %d times:\n%s", n, got.out)
	}
	history := got.out[strings.Index(got.out, "\nHistory\n")+len("\nHistory\n"):]
	lines := strings.Split(strings.TrimRight(history, "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "holds    go.mod contains") || !strings.Contains(lines[1], "broken   go.mod does not contain") {
		t.Errorf("the history reads, oldest first:\n%s", history)
	}

	// A blocked fact has no result yet, but it still says why.
	got = s.ok("why", s.blocked.ID)
	if n := strings.Count(got.out, pad+"check: "); n != 1 {
		t.Errorf("the check of a blocked fact is shown %d times:\n%s", n, got.out)
	}
	says(t, "why, of a blocked fact", got.out, "BLOCKED   "+s.blocked.ID, "not checked: ", "stored ")

	// Somebody who vouched for a note is named.
	s.ok("vouch", s.note.ID, "--as", "sara")
	says(t, "why, after a vouch", calm(s.ok("why", s.note.ID).out), "\n"+pad+"vouched for by sara, just now")
}

func TestWhyOfAnIdNobodyHasExitsOne(t *testing.T) {
	s := stocked(t)
	for _, id := range []string{"f-zzzzzz", "nonsense", strings.ToUpper(s.note.ID) + "x"} {
		got := s.run("why", id)
		if want := `holdfast: there is no fact with the id "` + id + `"` + "\n"; got.code != 1 || got.out != "" || got.err != want {
			t.Errorf("why %s: exit %d, %q, %q; want exit 1 and %q", id, got.code, got.out, got.err, want)
		}
	}
	for _, args := range [][]string{{"why"}, {"why", s.note.ID, s.proved.ID}, {"vouch"}, {"vouch", s.note.ID, s.proved.ID}, {"retract", "--reason", "x"}, {"retract", s.note.ID, s.proved.ID, "--reason", "x"}} {
		got := s.run(args...)
		if got.code != 2 || got.out != "" || !strings.HasPrefix(got.err, "holdfast: name one fact by its id\nUsage: holdfast "+args[0]) {
			t.Errorf("holdfast %v: exit %d, %q, %q; want exit 2", args, got.code, got.out, got.err)
		}
	}
	if l := s.listed(); l.Counts.Total != 3 || len(l.Gone) != 0 {
		t.Errorf("commands that named no one fact changed the store: %+v, %d gone", l.Counts, len(l.Gone))
	}
}

func TestWhyJSONIsTheFactInFull(t *testing.T) {
	s := stocked(t)
	v := s.why(s.proved.ID)
	if v.ID != s.proved.ID || v.Claim != goClaim || v.Check != goCheck || v.By != "alex" || v.Status != "holds" || len(v.History) != 1 || strings.Join(v.Tags, ",") != "build" {
		t.Errorf("why --json printed %+v", v)
	}
	// A fact that was withdrawn can still be asked about.
	s.ok("retract", s.note.ID, "--reason", "we deploy every day now", "--as", "sara")
	gone := s.why(s.note.ID)
	if gone.Retracted == nil || gone.Retracted.By != "sara" || gone.Retracted.Reason != "we deploy every day now" {
		t.Errorf("the withdrawn fact carries %+v", gone.Retracted)
	}
	says(t, "why, of a withdrawn fact", calm(s.ok("why", s.note.ID).out),
		"RETRACTED "+s.note.ID+"  Deploys go out on Fridays\n"+pad+"retracted just now by sara: we deploy every day now\n")
}

// --- vouch and retract ---

func TestVouchRecordsWhoSaysANoteIsStillSo(t *testing.T) {
	s := stocked(t)
	// The clock the store keeps is to the millisecond: a moment later is later.
	time.Sleep(3 * time.Millisecond)
	got := s.ok("vouch", s.note.ID, "--as", "sara")
	want := "Vouched for. Its time to live starts again from now.\nUNPROVEN  " + s.note.ID + "  Deploys go out on Fridays\n" +
		pad + "a note by alex, confirmed by sara, under a minute old; it has no check, so nothing proves it\n"
	if got.out != want {
		t.Errorf("vouch said:\n%s\nwant:\n%s", got.out, want)
	}
	v := s.why(s.note.ID)
	if v.VouchedBy != "sara" || v.By != "alex" || v.NotedAt == nil || !v.NotedAt.After(s.note.Created) {
		t.Errorf("after the vouch the note is %+v", v)
	}
	if v.StaleAt == nil || !v.StaleAt.After(*s.note.StaleAt) {
		t.Errorf("its life did not start again: stale at %v, was %v", v.StaleAt, s.note.StaleAt)
	}
	// With no name given, it is whoever runs the command.
	s.ok("vouch", s.note.ID)
	if v := s.why(s.note.ID); v.VouchedBy != "alex" {
		t.Errorf("vouched by %q, want alex", v.VouchedBy)
	}
}

func TestVouchRefusesAFactThatProvesItself(t *testing.T) {
	s := stocked(t)
	for _, id := range []string{s.proved.ID, s.blocked.ID} {
		got := s.run("vouch", id)
		if want := "holdfast: " + id + " has a check, so it proves itself; there is nothing to vouch for\n"; got.code != 1 || got.out != "" || got.err != want {
			t.Errorf("vouch for a checked fact: exit %d, %q, %q; want exit 1 and %q", got.code, got.out, got.err, want)
		}
	}
	got := s.run("vouch", "f-zzzzzz")
	if got.code != 1 || got.err != `holdfast: there is no fact with the id "f-zzzzzz"`+"\n" {
		t.Errorf("vouch for a fact nobody has: exit %d, %q", got.code, got.err)
	}
	if v := s.why(s.proved.ID); v.VouchedBy != "" {
		t.Errorf("a refused vouch was recorded: %q", v.VouchedBy)
	}
}

func TestRetractNeedsAReason(t *testing.T) {
	s := stocked(t)
	for _, args := range [][]string{
		{"retract", s.note.ID},
		{"retract", s.note.ID, "--reason", ""},
		{"retract", s.note.ID, "--reason", "   "},
	} {
		got := s.run(args...)
		want := "holdfast: say why " + s.note.ID + " is being retracted, in a few words: the reason is kept with it\n"
		if got.code != 1 || got.out != "" || got.err != want {
			t.Errorf("holdfast %q: exit %d, %q, %q; want exit 1 and %q", args, got.code, got.out, got.err, want)
		}
	}
	if l := s.listed(); l.Counts.Total != 3 || len(l.Gone) != 0 {
		t.Errorf("a retraction without a reason went through: %+v, %d gone", l.Counts, len(l.Gone))
	}
}

func TestRetractWithdrawsAFactAndKeepsTheReason(t *testing.T) {
	s := stocked(t)
	got := s.ok("retract", s.proved.ID, "--reason", "the project moved to Go 1.25", "--as", "sara")
	want := "RETRACTED " + s.proved.ID + "  " + goClaim + "\n" + pad + "retracted just now by sara: the project moved to Go 1.25\n"
	if got.out != want {
		t.Errorf("retract said:\n%s\nwant:\n%s", got.out, want)
	}
	l := s.listed()
	if l.Counts.Total != 2 || len(l.Gone) != 1 || l.Gone[0].ID != s.proved.ID {
		t.Fatalf("after the retraction: %+v live, gone %+v", l.Counts, l.Gone)
	}
	// It is out of every answer that is about what is true now.
	for _, args := range [][]string{{"list"}, {"check"}, {"recall", "pinned"}, {"brief"}, {"export"}} {
		if got := s.ok(args...); strings.Contains(got.out, s.proved.ID) {
			t.Errorf("holdfast %v still shows the withdrawn fact:\n%s", args, got.out)
		}
	}

	// It cannot be withdrawn twice, and the first reason stands.
	again := s.run("retract", s.proved.ID, "--reason", "changed my mind")
	if again.code != 1 || again.out != "" || again.err != "holdfast: "+s.proved.ID+" was already retracted\n" {
		t.Errorf("a second retraction: exit %d, %q, %q", again.code, again.out, again.err)
	}
	if r := s.why(s.proved.ID).Retracted; r == nil || r.Reason != "the project moved to Go 1.25" || r.By != "sara" {
		t.Errorf("the retraction now reads %+v", r)
	}
	got = s.run("retract", "f-zzzzzz", "--reason", "it is gone")
	if got.code != 1 || got.err != `holdfast: there is no fact with the id "f-zzzzzz"`+"\n" {
		t.Errorf("retracting a fact nobody has: exit %d, %q", got.code, got.err)
	}
}

// --- brief and export ---

func TestBriefTellsANewSessionWhatHoldsAndWhatDoesNot(t *testing.T) {
	s := stocked(t)
	// The brief runs every check itself: nobody has to run check first.
	s.breakGo()
	s.write("VERSION", "1.0\n")
	version := s.add("The version file exists", "file VERSION")

	got := s.ok("brief")
	want := "Holdfast, this project's memory: 4 facts: 1 broken, 1 blocked, 1 unproven, 1 holds. Every check was run just now.\n\n" +
		brokenText(s.proved) + "\n" + s.blockedText() + "\n" + noteText(s.note) + "\n\n" +
		"Proved and holding:\n  " + version.ID + "  The version file exists\n\n" +
		"Before relying on any of this later in the session, ask again with the recall tool: it proves each fact again first.\n"
	if calm(got.out) != want {
		t.Errorf("brief said:\n%s\nwant:\n%s", got.out, want)
	}
}

func TestBriefDoesNotClaimChecksItNeverRan(t *testing.T) {
	d := project(t)
	note := d.add("Deploys go out on Fridays", "")
	got := d.ok("brief")
	want := "Holdfast, this project's memory: 1 fact: 1 unproven.\n\n" + noteText(note) + "\n\n" +
		"Before relying on any of this later in the session, ask again with the recall tool: it proves each fact again first.\n"
	if got.out != want {
		t.Errorf("brief said:\n%s\nwant:\n%s", got.out, want)
	}
}

func TestExportWritesTheFactsAsMarkdown(t *testing.T) {
	d := project(t)
	if got := d.ok("export"); !strings.HasPrefix(got.out, "# What Holdfast knows\n\nno facts.") {
		t.Errorf("an empty store exports as:\n%s", got.out)
	}

	s := stocked(t)
	got := s.ok("export")
	file := filepath.Join(s.outside, "data.txt")
	want := "# What Holdfast knows\n\n" +
		"3 facts: 1 blocked, 1 unproven, 1 holds. Statuses are as of each fact's last check; run `holdfast check` to prove them again.\n\n" +
		"## Could not be checked\n\n" +
		"- **The data is there** (`" + s.blocked.ID + "`)  \n" +
		"  not checked: " + file + " is outside the folders the policy allows. A person can allow it: holdfast allow files " + s.outside + "\n\n" +
		"## Notes nobody has proved\n\n" +
		"- **Deploys go out on Fridays** (`" + s.note.ID + "`)  \n" +
		"  a note by alex, under a minute old, with no check\n\n" +
		"## Holds\n\n" +
		"- **" + goClaim + "** (`" + s.proved.ID + "`)  \n" +
		"  proof: go.mod contains \"go 1.24\" (line 3)\n"
	if got.out != want {
		t.Errorf("export wrote:\n%s\nwant:\n%s", got.out, want)
	}

	// Without --check it writes what was last known; with it, what is so now.
	s.breakGo()
	if got := s.ok("export"); got.out != want {
		t.Errorf("export without --check ran the checks:\n%s", got.out)
	}
	got = s.ok("export", "--check")
	says(t, "export --check", got.out,
		"3 facts: 1 broken, 1 blocked, 1 unproven.",
		"## No longer true\n\n- **"+goClaim+"** (`"+s.proved.ID+"`)  \n  proof failed: go.mod does not contain \"go 1.24\"\n")
	lacks(t, "export --check", got.out, "## Holds")
	if strings.Index(got.out, "## No longer true") > strings.Index(got.out, "## Could not be checked") {
		t.Errorf("what is no longer true does not come first:\n%s", got.out)
	}
	if got := s.run("export", "everything"); got.code != 2 || !strings.Contains(got.err, "export takes no arguments") {
		t.Errorf("export with an argument: exit %d, %q", got.code, got.err)
	}
}

// --- compact ---

func TestCompactRewritesTheEvidenceAndNeverTheFacts(t *testing.T) {
	s := stocked(t)
	// A history: the fact breaks and mends a few times.
	for i := 0; i < 3; i++ {
		s.breakGo()
		s.run("check")
		s.write("go.mod", goMod)
		s.ok("check")
	}
	facts := filepath.Join(s.cwd, ".holdfast", "facts.jsonl")
	evidence := filepath.Join(s.cwd, ".holdfast", "evidence.jsonl")
	journal := read(t, facts)
	before := s.listed()
	was, err := os.Stat(evidence)
	if err != nil {
		t.Fatal(err)
	}

	got := s.ok("compact")
	is, err := os.Stat(evidence)
	if err != nil {
		t.Fatal(err)
	}
	want := "evidence.jsonl: " + size(is.Size()) + ", was " + size(was.Size()) + ". The facts themselves are never rewritten.\n"
	if got.out != want {
		t.Errorf("compact said %q, want %q", got.out, want)
	}
	if is.Size() > was.Size() || is.Size() == 0 {
		t.Errorf("the evidence is %d bytes after compacting, was %d", is.Size(), was.Size())
	}
	if read(t, facts) != journal {
		t.Error("compact rewrote facts.jsonl")
	}

	// Nothing that is known was lost.
	after := s.listed()
	if len(after.Facts) != len(before.Facts) || after.Counts != before.Counts {
		t.Fatalf("after compacting: %+v, was %+v", after.Counts, before.Counts)
	}
	for i, v := range after.Facts {
		b := before.Facts[i]
		if v.ID != b.ID || v.Status != b.Status || v.Detail != b.Detail || len(v.History) != len(b.History) || (v.Held == nil) != (b.Held == nil) {
			t.Errorf("%s changed in compacting:\n%+v\nwas:\n%+v", b.ID, v, b)
		}
	}
	if got := s.ok("check"); !strings.Contains(got.out, "2 facts: 1 blocked, 1 holds.") {
		t.Errorf("check after compacting said:\n%s", got.out)
	}
	if got := s.run("compact", "now"); got.code != 2 || !strings.Contains(got.err, "compact takes no arguments") {
		t.Errorf("compact with an argument: exit %d, %q", got.code, got.err)
	}
}

func TestSizesAreWrittenForPeople(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 bytes", 1: "1 bytes", 834: "834 bytes", 999: "999 bytes",
		1000: "1.0 kB", 1536: "1.5 kB", 250_000: "250.0 kB",
		1_000_000: "1.0 MB", 12_345_678: "12.3 MB",
	} {
		if got := size(n); got != want {
			t.Errorf("size(%d) = %q, want %q", n, got, want)
		}
	}
}

// --- a journal with damage, and colour ---

func TestDamagedJournalLinesAreSkippedAndMentioned(t *testing.T) {
	s := stocked(t)
	journal := filepath.Join(s.cwd, ".holdfast", "facts.jsonl")
	damage := func(line string) {
		t.Helper()
		f, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}

	damage("this is not json")
	one := "holdfast: 1 damaged journal line was skipped; the rest of the memory is intact\n"
	for _, args := range [][]string{{"list"}, {"check"}, {"recall", "pinned"}, {"why", s.note.ID}, {"export"}, {"brief"}, {"compact"}} {
		got := s.run(args...)
		if got.code != 0 || got.err != one {
			t.Errorf("holdfast %v with one damaged line: exit %d, %q; want exit 0 and %q", args, got.code, got.err, one)
		}
		if got.out == "" {
			t.Errorf("holdfast %v with one damaged line printed nothing", args)
		}
	}
	var l engine.Listed
	got := s.run("list", "--json")
	decode(t, got.out, &l)
	if l.Counts.Total != 3 {
		t.Errorf("with a damaged line the store holds %+v, want the 3 facts that can be read", l.Counts)
	}

	// The memory still takes new facts, and says so every time until mended.
	damage(`{"id": 7}`)
	two := "holdfast: 2 damaged journal lines were skipped; the rest of the memory is intact\n"
	got = s.run("add", "The module file exists", "--check", "file go.mod")
	if got.code != 0 || got.err != two || !strings.HasPrefix(got.out, "Stored, and proved just now.\n") {
		t.Errorf("add with two damaged lines: exit %d, %q, %q", got.code, got.out, got.err)
	}
	got = s.run("list", "--json")
	decode(t, got.out, &l)
	if got.err != two || l.Counts.Total != 4 {
		t.Errorf("after adding to a damaged journal: %q, %+v", got.err, l.Counts)
	}
}

func TestStatusWordsAreColouredOnlyOnATerminal(t *testing.T) {
	s := stocked(t)
	s.breakGo()
	s.run("check")
	s.ok("retract", s.note.ID, "--reason", "we deploy every day now")
	if got := s.ok("add", "A new note"); strings.Contains(got.out, "\x1b") {
		t.Errorf("add wrote colour codes to something that is not a terminal:\n%q", got.out)
	}
	// Commands that read, so that asking twice gives the same answer.
	commands := [][]string{
		{"list", "--all"}, {"list", "--status", "blocked"}, {"check"}, {"check", "--quiet"}, {"recall", "pinned"},
		{"why", s.proved.ID}, {"brief"}, {"export"}, {"policy"}, {"help"},
	}

	plain := map[string]string{}
	for _, args := range commands {
		got := s.run(args...)
		if strings.Contains(got.out+got.err, "\x1b") {
			t.Errorf("holdfast %v wrote colour codes to something that is not a terminal:\n%q", args, got.out+got.err)
		}
		plain[strings.Join(args, " ")] = calm(got.out)
	}

	s.color = true
	for _, args := range commands {
		got := s.run(args...)
		// Colour changes how the words look and nothing else.
		if stripped := colour.ReplaceAllString(calm(got.out), ""); stripped != plain[strings.Join(args, " ")] {
			t.Errorf("holdfast %v in colour reads differently:\n%s\nplain:\n%s", args, stripped, plain[strings.Join(args, " ")])
		}
	}
	got := s.ok("list", "--all")
	says(t, "list in colour", got.out,
		"\x1b[1;31mBROKEN\x1b[0m    "+s.proved.ID,
		"\x1b[35mBLOCKED\x1b[0m   "+s.blocked.ID,
		"\x1b[2mUNPROVEN\x1b[0m  ",
		"\x1b[2mRETRACTED\x1b[0m "+s.note.ID,
		"\x1b[2m"+pad+"check: "+goCheck+"\x1b[0m")
	says(t, "add in colour", s.ok("add", "Another new note").out, "\x1b[2mUNPROVEN\x1b[0m  ")
	s.write("go.mod", goMod)
	mended := s.ok("check").out
	says(t, "check in colour", mended, "\x1b[32mHOLDS\x1b[0m     "+s.proved.ID)
	// The advice under the facts starts with the same words and is left plain.
	says(t, "check in colour", mended, "\n"+blockedAdvice)

	// What a program reads is never coloured.
	for _, args := range [][]string{{"list", "--json", "--all"}, {"check", "--json"}, {"recall", "--json"}, {"why", "--json", s.proved.ID}, {"add", "--json", "One more note"}, {"export"}} {
		if got := s.run(args...); strings.Contains(got.out, "\x1b") || got.out == "" {
			t.Errorf("holdfast %v on a terminal printed colour codes, or nothing:\n%q", args, got.out)
		}
	}
}

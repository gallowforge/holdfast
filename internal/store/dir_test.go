//go:build unix

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
)

func init() {
	kinds["dir"] = func(t *testing.T) (Store, func() Store) {
		d := newDir(t)
		return d, func() Store { return another(t, d) }
	}
}

// isolate points everything a directory store keeps outside the project at
// folders that belong to this test, so nothing lands in the real home or
// config folder.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOLDFAST_STATE", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
}

// newDir sets up a fresh store in a project folder of its own.
func newDir(t *testing.T) *Dir {
	t.Helper()
	isolate(t)
	d, err := Init(filepath.Join(t.TempDir(), DirName), policy.Default())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// another opens the same store folder a second time, the way another process
// would: it knows nothing but what the files say.
func another(t *testing.T, d *Dir) *Dir {
	t.Helper()
	again, err := Open(d.Path(), policy.Default())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { again.Close() })
	return again
}

func readFile(t testing.TB, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t testing.TB, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

// lines returns the lines of a journal. It fails the test if the file does
// not end with a newline or if any line is not one whole JSON event: a torn
// or interleaved write would show up as exactly that.
func lines(t testing.TB, path string) []Event {
	t.Helper()
	text := readFile(t, path)
	if text == "" {
		return nil
	}
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("%s does not end with a newline", filepath.Base(path))
	}
	var out []Event
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("%s line %d is not one JSON event (%v): %q", filepath.Base(path), i+1, err, line)
		}
		out = append(out, e)
	}
	return out
}

func names(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func line(t testing.TB, e Event) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func TestInitCreatesTheStoreFolderWithEveryFile(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "project", DirName)
	d, err := Init(path, policy.Default())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer d.Close()

	want := []string{".gitattributes", ".gitignore", "evidence.jsonl", "facts.jsonl", "lock", "policy"}
	if got := names(t, d.Path()); !slices.Equal(got, want) {
		t.Fatalf("the store folder holds %v, want %v", got, want)
	}
	for name, content := range map[string]string{
		"facts.jsonl":    "",
		"evidence.jsonl": "",
		// Facts are shared and merge line by line; proof and the lock stay local.
		".gitattributes": "facts.jsonl merge=union\n",
		".gitignore":     "evidence.jsonl\nlock\n",
		"policy":         policy.Default().String(),
	} {
		if got := readFile(t, filepath.Join(d.Path(), name)); got != content {
			t.Errorf("%s holds %q, want %q", name, got, content)
		}
	}
	if filepath.Base(d.Path()) != DirName || d.Root() != filepath.Dir(d.Path()) {
		t.Errorf("Path() = %s, Root() = %s: the root must be the folder that holds the store", d.Path(), d.Root())
	}
	if all := mustFacts(t, d); len(all) != 0 || d.Damaged() != 0 {
		t.Errorf("a new store has %d facts and %d damaged lines", len(all), d.Damaged())
	}
}

func TestInitASecondTimeChangesNothing(t *testing.T) {
	d := newDir(t)
	f := mustAdd(t, d, note("the team deploys on Fridays", t0), nil)
	custom := policy.Default().String() + "net example.com\n"
	if err := d.SetPolicyText(custom); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(d.Path(), ".gitignore"), "evidence.jsonl\nlock\nmine\n")

	again, err := Init(d.Path(), policy.Default())
	if err != nil {
		t.Fatalf("a second Init: %v", err)
	}
	defer again.Close()
	if got := mustGet(t, again, f.ID); got.Claim != f.Claim {
		t.Errorf("the fact did not survive a second Init: %+v", got)
	}
	if got := readFile(t, filepath.Join(d.Path(), PolicyFile)); got != custom {
		t.Errorf("a second Init rewrote the policy file: %q", got)
	}
	if got := readFile(t, filepath.Join(d.Path(), ".gitignore")); !strings.HasSuffix(got, "mine\n") {
		t.Errorf("a second Init rewrote .gitignore: %q", got)
	}
	if _, why := again.Policy(); why != "" {
		t.Errorf("the approved policy lost its approval: %s", why)
	}
}

func TestInitCompletesAStoreThatIsMissingFiles(t *testing.T) {
	d := newDir(t)
	f := mustAdd(t, d, note("the team deploys on Fridays", t0), nil)
	for _, name := range []string{".gitignore", ".gitattributes", EvidenceFile, "lock"} {
		if err := os.Remove(filepath.Join(d.Path(), name)); err != nil {
			t.Fatal(err)
		}
	}
	again, err := Init(d.Path(), policy.Default())
	if err != nil {
		t.Fatalf("Init over a partial store: %v", err)
	}
	defer again.Close()
	want := []string{".gitattributes", ".gitignore", "evidence.jsonl", "facts.jsonl", "lock", "policy"}
	if got := names(t, d.Path()); !slices.Equal(got, want) {
		t.Errorf("the store folder holds %v, want %v", got, want)
	}
	if _, err := again.Get(f.ID); err != nil {
		t.Errorf("the fact that was there is gone: %v", err)
	}
}

// A policy that came with a cloned repository was written by someone else.
// Setting the store up on this machine must not approve it.
func TestInitDoesNotApproveAPolicyItDidNotWrite(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), DirName)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(path, PolicyFile), "files .\nnet public\ncmd rm **\n")
	d, err := Init(path, policy.Default())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer d.Close()
	p, why := d.Policy()
	if why == "" {
		t.Fatal("a policy nobody approved on this machine is in force")
	}
	if len(p.Cmds) != 0 || p.String() != policy.Default().String() {
		t.Errorf("the policy in force is not the base one:\n%s", p)
	}
}

func TestFindWalksUpToTheNearestStore(t *testing.T) {
	isolate(t)
	top := t.TempDir()
	if real, err := filepath.EvalSymlinks(top); err == nil {
		top = real
	}
	if p, ok := Find(top); ok {
		t.Skipf("there is a store above the test's temporary folder (%s), so this cannot be tested here", p)
	}
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{top}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	outer := mk("outer", DirName)
	deep := mk("outer", "a", "b", "c")
	inner := mk("outer", "inner", DirName)
	innerDeep := mk("outer", "inner", "x")
	bare := mk("bare", "a")
	// A file called .holdfast is not a store, and must not stop the walk.
	writeFile(t, filepath.Join(mk("outer", "odd"), DirName), "not a folder")
	odd := filepath.Join(top, "outer", "odd")

	tests := []struct {
		name  string
		start string
		want  string
		found bool
	}{
		{"the folder that holds the store", filepath.Join(top, "outer"), outer, true},
		{"several folders below it", deep, outer, true},
		{"the nearest store wins", innerDeep, inner, true},
		{"inside the store folder itself", outer, outer, true},
		{"a file named like a store is walked past", odd, outer, true},
		{"no store anywhere above", bare, "", false},
		{"a folder that does not exist yet, below a store", filepath.Join(deep, "not", "made"), outer, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Find(tc.start)
			if ok != tc.found || got != tc.want {
				t.Errorf("Find(%s) = %q, %v; want %q, %v", tc.start, got, ok, tc.want, tc.found)
			}
		})
	}
	t.Run("a relative start counts from the working directory", func(t *testing.T) {
		t.Chdir(deep)
		if got, ok := Find("."); !ok || got != outer {
			t.Errorf("Find(.) from %s = %q, %v; want %q", deep, got, ok, outer)
		}
	})
}

func TestOpenNeedsAFolderThatExists(t *testing.T) {
	isolate(t)
	top := t.TempDir()
	if _, err := Open(filepath.Join(top, "missing", DirName), policy.Default()); err == nil {
		t.Error("Open of a folder that does not exist succeeded")
	}
	file := filepath.Join(top, DirName)
	writeFile(t, file, "a file, not a folder")
	_, err := Open(file, policy.Default())
	if err == nil || !strings.Contains(err.Error(), "is not a folder") {
		t.Errorf("Open of a file = %v, want it to say that is not a folder", err)
	}
	if got := names(t, top); !slices.Equal(got, []string{DirName}) {
		t.Errorf("a failed Open left files behind: %v", got)
	}
}

func TestOpenFollowsASymbolicLinkToTheRealFolder(t *testing.T) {
	d := newDir(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(d.Path(), link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	via, err := Open(link, policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer via.Close()
	if via.Path() != d.Path() {
		t.Errorf("opened through a link, Path() = %s, want the real folder %s", via.Path(), d.Path())
	}
	// The approval is kept by real path, so it holds through the link too.
	if _, why := via.Policy(); why != "" {
		t.Errorf("the policy is not in force through the link: %s", why)
	}
}

// Everything a store is told must still be there for the next process.
func TestEverythingSurvivesClosingAndOpeningAgain(t *testing.T) {
	d := newDir(t)
	first := evidence(fact.Holds, at(time.Second))
	port := checked(t, "the API listens on port 8080", "port localhost:8080", t0)
	port.Tags = []string{"api", "ports"}
	a := mustAdd(t, d, port, &first)
	ttl := note("the release branch is cut on Mondays", at(time.Minute))
	ttl.TTL = fact.Seconds(7 * 24 * 3600)
	b := mustAdd(t, d, ttl, nil)
	c := mustAdd(t, d, checked(t, "there is no .env file", "not file .env", at(2*time.Minute)), nil)
	sum := checked(t, "the lock file has not changed", "unchanged go.sum", at(3*time.Minute))
	sum.Check.Sum = strings.Repeat("ab", 32)
	e := mustAdd(t, d, sum, nil)

	mustRecord(t, d, a.ID, evidence(fact.Broken, at(10*time.Minute)))
	mustRecord(t, d, c.ID, evidence(fact.Blocked, at(11*time.Minute)))
	if err := d.Vouch(b.ID, "alex", at(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := d.Retract(c.ID, fact.Retraction{At: at(2 * time.Hour), By: "alex", Reason: "it has one now"}); err != nil {
		t.Fatal(err)
	}
	moved := checked(t, "the API listens on port 9090", "port localhost:9090", at(3*time.Hour))
	moved.Replaces = a.ID
	second := evidence(fact.Holds, at(3*time.Hour))
	mustAdd(t, d, moved, &second)

	want := asJSON(t, mustFacts(t, d))
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	again, err := Open(d.Path(), policy.Default())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer again.Close()
	if got := asJSON(t, mustFacts(t, again)); got != want {
		t.Errorf("the store reads back differently.\nbefore:\n%s\nafter:\n%s", want, got)
	}
	if again.Damaged() != 0 {
		t.Errorf("%d damaged lines in journals only the store wrote", again.Damaged())
	}

	got := mustGet(t, again, e.ID)
	if got.Check.Sum != strings.Repeat("ab", 32) || got.Check.Short() != "unchanged go.sum" {
		t.Errorf("the recorded digest did not survive: %+v", got.Check)
	}
	for _, e := range lines(t, filepath.Join(d.Path(), FactsFile)) {
		if e.Op == "evidence" {
			t.Error("evidence was written to facts.jsonl, which is meant to be committed")
		}
	}
	for _, e := range lines(t, filepath.Join(d.Path(), EvidenceFile)) {
		if e.Op != "evidence" {
			t.Errorf("a %q event was written to evidence.jsonl, which never leaves the machine", e.Op)
		}
	}
}

// Evidence reaches the journal when it changes what is known, or when what the
// journal has is at least Refresh old. A check that keeps passing must not
// grow the journal on every recall.
func TestEvidenceIsJournaledOnAChangeOrWhenTheLastWrittenIsOld(t *testing.T) {
	if Refresh != 5*time.Minute {
		t.Fatalf("Refresh is %v; the promise is 5 minutes", Refresh)
	}
	d := newDir(t)
	journal := filepath.Join(d.Path(), EvidenceFile)
	first := evidence(fact.Holds, t0)
	f := mustAdd(t, d, checked(t, "go.mod is there", "file go.mod", t0), &first)
	if n := len(lines(t, journal)); n != 1 {
		t.Fatalf("the first evidence wrote %d lines, want 1", n)
	}

	steps := []struct {
		name    string
		status  fact.Status
		after   time.Duration
		written bool
	}{
		{"the same answer a minute later", fact.Holds, time.Minute, false},
		{"the same answer just short of five minutes", fact.Holds, 5*time.Minute - time.Millisecond, false},
		{"the same answer at five minutes exactly", fact.Holds, 5 * time.Minute, true},
		{"the same answer soon after that", fact.Holds, 9 * time.Minute, false},
		{"a different answer straight away", fact.Broken, 9*time.Minute + time.Second, true},
		{"the new answer again", fact.Broken, 12 * time.Minute, false},
		{"another change", fact.Blocked, 12*time.Minute + time.Second, true},
		{"and back", fact.Holds, 12*time.Minute + 2*time.Second, true},
		{"five minutes after the last line written", fact.Holds, 17*time.Minute + 2*time.Second, true},
	}
	want := 1
	for _, st := range steps {
		ev := evidence(st.status, at(st.after))
		mustRecord(t, d, f.ID, ev)
		if st.written {
			want++
		}
		if got := len(lines(t, journal)); got != want {
			t.Fatalf("%s: the journal has %d lines, want %d", st.name, got, want)
		}
		// Written or not, this process knows what it just saw.
		got := mustGet(t, d, f.ID)
		if got.Last == nil || got.Last.Status != st.status || !got.Last.At.Equal(ev.At) {
			t.Fatalf("%s: last evidence is %+v, want %s at %v", st.name, got.Last, st.status, ev.At)
		}
	}
}

func TestEvidenceKeptOutOfTheJournalIsStillKnownToTheProcessThatSawIt(t *testing.T) {
	d := newDir(t)
	first := evidence(fact.Holds, t0)
	f := mustAdd(t, d, checked(t, "go.mod is there", "file go.mod", t0), &first)
	mustRecord(t, d, f.ID, evidence(fact.Holds, at(time.Minute)))

	if got := mustGet(t, d, f.ID); !got.Last.At.Equal(at(time.Minute)) || !got.Held.Equal(at(time.Minute)) {
		t.Errorf("this process forgot what it saw: last %v, held %v", got.Last.At, got.Held)
	}
	if got := mustGet(t, another(t, d), f.ID); !got.Last.At.Equal(t0) {
		t.Errorf("another process sees evidence from %v, want only what was written (%v)", got.Last.At, t0)
	}
	// Reading again, with nothing new in the journal, must not lose it.
	mustFacts(t, d)
	if got := mustGet(t, d, f.ID); !got.Last.At.Equal(at(time.Minute)) {
		t.Errorf("a later read lost the unwritten evidence: last %v", got.Last.At)
	}
}

func TestTwoHandlesSeeEachOthersWrites(t *testing.T) {
	a := newDir(t)
	b := another(t, a)
	fa := mustAdd(t, a, checked(t, "written through the first handle", "file go.mod", t0), nil)
	fb := mustAdd(t, b, note("written through the second handle", t0), nil)

	for name, h := range map[string]*Dir{"first": a, "second": b} {
		all := mustFacts(t, h)
		if len(all) != 2 || all[0].ID != fa.ID || all[1].ID != fb.ID {
			t.Errorf("the %s handle lists %d facts, want both in the order they were written", name, len(all))
		}
	}
	mustRecord(t, b, fa.ID, evidence(fact.Broken, at(time.Minute)))
	if got := mustGet(t, a, fa.ID); got.Last == nil || got.Last.Status != fact.Broken {
		t.Errorf("the first handle does not see evidence the second wrote: %+v", got.Last)
	}
	if err := a.Retract(fb.ID, fact.Retraction{At: at(time.Hour), Reason: "wrong"}); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, b, fb.ID); got.Retracted == nil {
		t.Error("the second handle does not see a retraction the first wrote")
	}
	in := note("replaces the first", at(time.Hour))
	in.Replaces = fa.ID
	mustAdd(t, a, in, nil)
	late := note("tries to replace it again", at(2*time.Hour))
	late.Replaces = fa.ID
	var rejected *Rejected
	if _, err := b.Add(late, nil); !errors.As(err, &rejected) {
		t.Errorf("a fact replaced through one handle was replaced again through the other: %v", err)
	}
}

func TestDamagedLinesAreSkippedAndCountedAndNeverFatal(t *testing.T) {
	good := Event{T: t0, Op: "add", ID: "f-abc234", Fact: &fact.Fact{Claim: "a checked fact", Check: &fact.Check{Kind: "file", Path: "go.mod"}, Created: t0}}
	noted := Event{T: t0, Op: "add", ID: "f-def567", Fact: &fact.Fact{Claim: "a note", Created: t0}}
	holds := Event{T: at(time.Minute), Op: "evidence", ID: "f-abc234", Ev: &fact.Evidence{Status: fact.Holds, Detail: "go.mod exists", At: at(time.Minute)}}

	tests := []struct {
		name     string
		facts    string
		evidence string
		damaged  int
	}{
		{"clean journals", "", "", 0},
		{"blank lines and stray spaces", "\n\n   \n\t\n", "\n  \n", 0},
		{"a line that is not JSON", "this is not json\n", "", 1},
		{"half a line, closed off", `{"t":"2026-03-01T09:00:00Z","op":"add","id":"f-x` + "\n", "", 1},
		{"a JSON value that is not an event", "[1,2,3]\n", "", 1},
		{"binary rubbish", "\x00\xff\xfe\x01\n", "", 1},
		{"the same fact twice, as a merge can leave it", line(t, good), "", 0},
		{"a fact with an id that is not one", line(t, Event{T: t0, Op: "add", ID: "seven", Fact: &fact.Fact{Claim: "odd"}}), "", 1},
		{"a fact with no claim", line(t, Event{T: t0, Op: "add", ID: "f-222222", Fact: &fact.Fact{}}), "", 1},
		{"a fact whose check is not well formed", line(t, Event{T: t0, Op: "add", ID: "f-222222", Fact: &fact.Fact{Claim: "odd", Check: &fact.Check{Kind: "teleport"}}}), "", 1},
		{"a retraction of a fact nobody has", line(t, Event{T: t0, Op: "retract", ID: "f-222222", Reason: "x"}), "", 1},
		{"a vouching for a fact nobody has", line(t, Event{T: t0, Op: "vouch", ID: "f-222222", By: "x"}), "", 1},
		{"an event of no known kind", line(t, Event{T: t0, Op: "celebrate", ID: "f-abc234"}), "", 1},
		{"evidence in the journal of facts", line(t, holds), "", 1},
		{"a fact in the journal of evidence", "", line(t, Event{T: t0, Op: "add", ID: "f-222222", Fact: &fact.Fact{Claim: "smuggled"}}), 1},
		{"a retraction in the journal of evidence", "", line(t, Event{T: t0, Op: "retract", ID: "f-abc234"}), 1},
		// Evidence stays behind when a fact is gone (another branch, a
		// teammate's machine): that is not damage.
		{"evidence for a fact this machine does not have", "", line(t, Event{T: t0, Op: "evidence", ID: "f-222222", Ev: &fact.Evidence{Status: fact.Holds, At: t0}}), 0},
		{"evidence for a note", "", line(t, Event{T: t0, Op: "evidence", ID: "f-def567", Ev: &fact.Evidence{Status: fact.Holds, At: t0}}), 0},
		{"rubbish in the journal of evidence", "", "][\n{\n", 2},
		{"several kinds at once", "nonsense\n" + line(t, good) + "{}\n", "more nonsense\n", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			path := filepath.Join(t.TempDir(), DirName)
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			// The damage sits between good lines, so skipping it must not
			// lose its neighbours.
			writeFile(t, filepath.Join(path, FactsFile), line(t, good)+tc.facts+line(t, noted))
			writeFile(t, filepath.Join(path, EvidenceFile), tc.evidence+line(t, holds))

			d, err := Open(path, policy.Default())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer d.Close()
			all, err := d.Facts()
			if err != nil {
				t.Fatalf("damage made the store unreadable: %v", err)
			}
			if len(all) != 2 || all[0].ID != "f-abc234" || all[1].ID != "f-def567" {
				t.Fatalf("the store reads %d facts, want the two good ones", len(all))
			}
			if all[0].Last == nil || all[0].Last.Status != fact.Holds {
				t.Errorf("the good evidence after the damage was lost: %+v", all[0].Last)
			}
			if all[1].Last != nil {
				t.Errorf("a note was given proof by a journal line: %+v", all[1].Last)
			}
			if got := d.Damaged(); got != tc.damaged {
				t.Errorf("Damaged() = %d, want %d", got, tc.damaged)
			}
			// Reading again must not count the same damage twice.
			mustFacts(t, d)
			if got := d.Damaged(); got != tc.damaged {
				t.Errorf("after a second read Damaged() = %d, want %d", got, tc.damaged)
			}
			// And a damaged store still takes new facts.
			added := mustAdd(t, d, note("added after the damage", at(time.Hour)), nil)
			if got := mustGet(t, another(t, d), added.ID); got.Claim != "added after the damage" {
				t.Errorf("a fact added to a damaged store did not read back: %+v", got)
			}
		})
	}
}

// A write that was cut off leaves a last line with no newline. It is not
// counted while it might still be in flight; the next append closes it off,
// and from then on it is one damaged line and its neighbours are whole.
func TestATornLastLineIsLeftAloneUntilTheNextAppendClosesItOff(t *testing.T) {
	tests := []struct {
		name    string
		journal string
		torn    string
	}{
		{"a fact cut short", FactsFile, `{"t":"2026-03-01T09:00:00Z","op":"add","id":"f-zz`},
		{"evidence cut short", EvidenceFile, `{"t":"2026-03-01T09:00:00Z","op":"evidence","id":"f-`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newDir(t)
			first := evidence(fact.Holds, t0)
			before := mustAdd(t, d, checked(t, "written before the crash", "file go.mod", t0), &first)
			appendFile(t, filepath.Join(d.Path(), tc.journal), tc.torn)

			reader := another(t, d)
			if all := mustFacts(t, reader); len(all) != 1 {
				t.Fatalf("with a torn last line the store reads %d facts, want 1", len(all))
			}
			if reader.Damaged() != 0 {
				t.Errorf("Damaged() = %d while the last line may still be being written, want 0", reader.Damaged())
			}

			second := evidence(fact.Holds, at(time.Minute))
			after := mustAdd(t, d, checked(t, "written after the crash", "file go.sum", at(time.Minute)), &second)
			mustRecord(t, d, before.ID, evidence(fact.Broken, at(2*time.Minute)))

			for name, h := range map[string]*Dir{"the writer": d, "the reader": reader, "a new process": another(t, d)} {
				all := mustFacts(t, h)
				if len(all) != 2 || all[0].ID != before.ID || all[1].ID != after.ID {
					t.Fatalf("%s reads %d facts, want the one before and the one after the torn line", name, len(all))
				}
				if all[0].Last == nil || all[0].Last.Status != fact.Broken || all[1].Last == nil || all[1].Last.Status != fact.Holds {
					t.Errorf("%s lost evidence next to the torn line: %+v, %+v", name, all[0].Last, all[1].Last)
				}
				if got := h.Damaged(); got != 1 {
					t.Errorf("%s counts %d damaged lines, want exactly the torn one", name, got)
				}
			}
			text := readFile(t, filepath.Join(d.Path(), tc.journal))
			if !strings.Contains(text, tc.torn+"\n") {
				t.Errorf("the torn line was not closed off with a newline:\n%s", text)
			}
		})
	}
}

// A line that arrived whole but for its newline is a whole event once the
// next append closes it off.
func TestALastLineMissingOnlyItsNewlineIsNotLost(t *testing.T) {
	d := newDir(t)
	whole := Event{T: t0, Op: "add", ID: "f-abc234", Fact: &fact.Fact{Claim: "all there but the newline", Created: t0}}
	appendFile(t, filepath.Join(d.Path(), FactsFile), strings.TrimSuffix(line(t, whole), "\n"))
	if all := mustFacts(t, d); len(all) != 0 {
		t.Fatalf("a line with no newline yet was read as a fact: %d facts", len(all))
	}
	mustAdd(t, d, note("the next fact", t0), nil)
	all := mustFacts(t, d)
	if len(all) != 2 || all[0].ID != "f-abc234" {
		t.Fatalf("the store reads %d facts, want both", len(all))
	}
	if d.Damaged() != 0 {
		t.Errorf("Damaged() = %d, want 0", d.Damaged())
	}
}

func TestAJournalThatWasReplacedIsReadAgainFromTheStart(t *testing.T) {
	kept := Event{T: t0, Op: "add", ID: "f-abc234", Fact: &fact.Fact{Claim: "on both branches", Created: t0}}
	other := Event{T: t0, Op: "add", ID: "f-def567", Fact: &fact.Fact{Claim: "only on the other branch", Created: t0}}
	tests := []struct {
		name   string
		change func(t *testing.T, path string)
	}{
		{"rewritten in place, as an editor does", func(t *testing.T, path string) {
			writeFile(t, path, line(t, kept)+line(t, other))
		}},
		{"replaced by another file, as git checkout does", func(t *testing.T, path string) {
			tmp := path + ".new"
			writeFile(t, tmp, line(t, kept)+line(t, other))
			if err := os.Rename(tmp, path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newDir(t)
			path := filepath.Join(d.Path(), FactsFile)
			writeFile(t, path, line(t, kept))
			gone := mustAdd(t, d, note("only on this branch, a good deal longer than the line that takes its place", t0), nil)
			if all := mustFacts(t, d); len(all) != 2 {
				t.Fatalf("%d facts before the change, want 2", len(all))
			}

			tc.change(t, path)

			all := mustFacts(t, d)
			if len(all) != 2 || all[0].ID != "f-abc234" || all[1].ID != "f-def567" {
				t.Fatalf("after the journal changed the store lists %d facts, want exactly what the file now says", len(all))
			}
			if _, err := d.Get(gone.ID); !errors.Is(err, ErrNotFound) {
				t.Errorf("a fact that is no longer in the journal is still remembered: %v", err)
			}
			if d.Damaged() != 0 {
				t.Errorf("Damaged() = %d after a clean replacement", d.Damaged())
			}
		})
	}
}

func TestAJournalThatWasDeletedMeansAnEmptyStore(t *testing.T) {
	d := newDir(t)
	first := evidence(fact.Holds, t0)
	mustAdd(t, d, checked(t, "go.mod is there", "file go.mod", t0), &first)
	for _, name := range []string{FactsFile, EvidenceFile} {
		if err := os.Remove(filepath.Join(d.Path(), name)); err != nil {
			t.Fatal(err)
		}
	}
	if all := mustFacts(t, d); len(all) != 0 {
		t.Errorf("%d facts remembered after both journals were deleted", len(all))
	}
	f := mustAdd(t, d, note("a fresh start", t0), nil)
	if got := mustFacts(t, another(t, d)); len(got) != 1 || got[0].ID != f.ID {
		t.Errorf("the store did not start again cleanly: %d facts", len(got))
	}
}

// history builds a fact with a long and varied past, so compaction has
// something to get wrong.
func history(t *testing.T, d *Dir, claim string, seed int, steps int) string {
	t.Helper()
	f := mustAdd(t, d, checked(t, claim, "file go.mod", t0), nil)
	cycle := []fact.Status{fact.Holds, fact.Holds, fact.Holds, fact.Broken, fact.Broken, fact.Blocked, fact.Holds, fact.Broken}
	for i := range steps {
		status := cycle[(i*(seed+1)+seed)%len(cycle)]
		// Three minutes apart: some of these reach the journal only because
		// they are a change, some only because the last line is old, some not
		// at all.
		mustRecord(t, d, f.ID, evidence(status, at(time.Duration(i+1)*3*time.Minute)))
	}
	return f.ID
}

func TestCompactRewritesOnlyTheEvidenceAndLosesNothing(t *testing.T) {
	d := newDir(t)
	for seed := range 7 {
		history(t, d, fmt.Sprintf("a fact with history %d", seed), seed, 30+seed*9)
	}
	mustAdd(t, d, note("a note among them", t0), nil)
	never := mustAdd(t, d, checked(t, "a check that has never run", "file never.txt", t0), nil)
	retracted := history(t, d, "a fact that was withdrawn", 3, 12)
	if err := d.Retract(retracted, fact.Retraction{At: at(100 * time.Hour), By: "alex", Reason: "no longer so"}); err != nil {
		t.Fatal(err)
	}

	factsPath, evidencePath := filepath.Join(d.Path(), FactsFile), filepath.Join(d.Path(), EvidenceFile)
	factsBefore := readFile(t, factsPath)
	evidenceBefore := readFile(t, evidencePath)
	want := asJSON(t, mustFacts(t, d))
	reader := another(t, d) // has read the old journal to its end
	mustFacts(t, reader)

	before, after, err := d.Compact()
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if before != int64(len(evidenceBefore)) {
		t.Errorf("Compact says the journal was %d bytes; it was %d", before, len(evidenceBefore))
	}
	if got := int64(len(readFile(t, evidencePath))); after != got {
		t.Errorf("Compact says the journal is now %d bytes; it is %d", after, got)
	}
	if after >= before {
		t.Errorf("the journal went from %d to %d bytes: compaction saved nothing", before, after)
	}
	if got := readFile(t, factsPath); got != factsBefore {
		t.Error("Compact rewrote facts.jsonl; the facts themselves must never be rewritten")
	}
	wantNames := []string{".gitattributes", ".gitignore", "evidence.jsonl", "facts.jsonl", "lock", "policy"}
	if got := names(t, d.Path()); !slices.Equal(got, wantNames) {
		t.Errorf("after Compact the folder holds %v, want %v (no temporary file left behind)", got, wantNames)
	}
	for _, e := range lines(t, evidencePath) {
		if e.Op != "evidence" || e.Ev == nil {
			t.Fatalf("the compacted journal holds a %q event", e.Op)
		}
	}

	for name, h := range map[string]*Dir{
		"the handle that compacted":         d,
		"a handle that was already reading": reader,
		"a new process":                     another(t, d),
	} {
		if got := asJSON(t, mustFacts(t, h)); got != want {
			t.Errorf("%s reads a different store after Compact.\nbefore:\n%s\nafter:\n%s", name, want, got)
		}
		if h.Damaged() != 0 {
			t.Errorf("%s counts %d damaged lines after Compact", name, h.Damaged())
		}
	}
	if got := mustGet(t, d, never.ID); got.Last != nil {
		t.Errorf("Compact invented evidence for a check that never ran: %+v", got.Last)
	}

	// Compacting what is already compact changes nothing more, and the store
	// keeps working.
	b2, a2, err := d.Compact()
	if err != nil || b2 != after || a2 != after {
		t.Errorf("a second Compact = %d, %d, %v; want %d, %d, nil", b2, a2, err, after, after)
	}
	f := mustAdd(t, d, checked(t, "added after compaction", "file go.sum", at(200*time.Hour)), nil)
	mustRecord(t, d, f.ID, evidence(fact.Holds, at(200*time.Hour)))
	if got := mustGet(t, reader, f.ID); got.Last == nil || got.Last.Status != fact.Holds {
		t.Errorf("evidence written after compaction is not seen by the other handle: %+v", got.Last)
	}
}

// What this process saw but did not write (Refresh) is state too: compaction
// is the moment it reaches the journal.
func TestCompactKeepsEvidenceThatWasOnlyInMemory(t *testing.T) {
	d := newDir(t)
	first := evidence(fact.Holds, t0)
	f := mustAdd(t, d, checked(t, "go.mod is there", "file go.mod", t0), &first)
	mustRecord(t, d, f.ID, evidence(fact.Broken, at(time.Minute)))   // written: a change
	mustRecord(t, d, f.ID, evidence(fact.Broken, at(2*time.Minute))) // not written
	want := asJSON(t, mustFacts(t, d))
	if _, _, err := d.Compact(); err != nil {
		t.Fatal(err)
	}
	if got := asJSON(t, mustFacts(t, another(t, d))); got != want {
		t.Errorf("a new process reads a different store after Compact.\nbefore:\n%s\nafter:\n%s", want, got)
	}
}

// "It last held ..." is shown for every broken fact. A fact that held once
// and has since changed between broken and blocked more times than the
// history keeps has no passing check left in its history; compaction must
// still not forget when it last held.
func TestCompactKeepsWhenAFactLastHeldEvenBeyondTheHistory(t *testing.T) {
	d := newDir(t)
	f := mustAdd(t, d, checked(t, "go.mod is there", "file go.mod", t0), nil)
	mustRecord(t, d, f.ID, evidence(fact.Holds, at(time.Minute)))
	mustRecord(t, d, f.ID, evidence(fact.Holds, at(10*time.Minute)))
	for i := range fact.MaxHistory + 5 {
		status := fact.Broken
		if i%2 == 1 {
			status = fact.Blocked
		}
		mustRecord(t, d, f.ID, evidence(status, at(time.Duration(i+1)*time.Hour)))
	}
	was := mustGet(t, d, f.ID)
	if !was.Held.Equal(at(10*time.Minute)) || strings.Contains(statuses(was.History), "holds") {
		t.Fatalf("the test did not build the case it means to: held %v, history %q", was.Held, statuses(was.History))
	}
	want := asJSON(t, was)
	if _, _, err := d.Compact(); err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]*Dir{"the handle that compacted": d, "a new process": another(t, d)} {
		got := mustGet(t, h, f.ID)
		if !got.Held.Equal(at(10 * time.Minute)) {
			t.Errorf("%s: after Compact the fact last held at %v, want %v", name, got.Held, at(10*time.Minute))
		}
		if asJSON(t, got) != want {
			t.Errorf("%s reads a different fact after Compact.\nbefore:\n%s\nafter:\n%s", name, want, asJSON(t, got))
		}
	}
}

func TestCompactOfAnEmptyStoreIsHarmless(t *testing.T) {
	d := newDir(t)
	before, after, err := d.Compact()
	if err != nil || before != 0 || after != 0 {
		t.Errorf("Compact of an empty store = %d, %d, %v; want 0, 0, nil", before, after, err)
	}
	mustAdd(t, d, note("still works", t0), nil)
	if all := mustFacts(t, another(t, d)); len(all) != 1 {
		t.Errorf("%d facts after compacting an empty store and adding one", len(all))
	}
}

// Evidence for facts this machine does not have is dead weight: compaction
// is where it goes.
func TestCompactDropsEvidenceNoFactOwns(t *testing.T) {
	d := newDir(t)
	first := evidence(fact.Holds, t0)
	f := mustAdd(t, d, checked(t, "go.mod is there", "file go.mod", t0), &first)
	appendFile(t, filepath.Join(d.Path(), EvidenceFile),
		line(t, Event{T: t0, Op: "evidence", ID: "f-222222", Ev: &fact.Evidence{Status: fact.Holds, At: t0}})+"rubbish\n")
	mustFacts(t, d)
	if d.Damaged() != 1 {
		t.Fatalf("Damaged() = %d before compaction, want 1", d.Damaged())
	}
	if _, _, err := d.Compact(); err != nil {
		t.Fatal(err)
	}
	got := lines(t, filepath.Join(d.Path(), EvidenceFile))
	if len(got) != 1 || got[0].ID != f.ID {
		t.Errorf("the compacted journal holds %d lines, want only the one for %s", len(got), f.ID)
	}
	if d.Damaged() != 0 {
		t.Errorf("Damaged() = %d after the damaged line was compacted away", d.Damaged())
	}
}

func TestADirStoreHoldsAtMost2000LiveFacts(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), DirName)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	// Written as one file rather than through Add: two thousand synced
	// appends would make this the slowest test by far.
	var journal strings.Builder
	alphabet := "23456789abcdefghjkmnpqrstvwxyz"
	for i := range MaxFacts {
		id := "f-22" + string([]byte{alphabet[i/900%30], alphabet[i/30%30], alphabet[i%30], 'z'})
		journal.WriteString(line(t, Event{T: t0, Op: "add", ID: id, Fact: &fact.Fact{Claim: fmt.Sprintf("fact number %d", i), Created: t0}}))
	}
	writeFile(t, filepath.Join(path, FactsFile), journal.String())
	d, err := Open(path, policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	all := mustFacts(t, d)
	if len(all) != MaxFacts || d.Damaged() != 0 {
		t.Fatalf("the prepared journal reads as %d facts with %d damaged lines, want %d and 0", len(all), d.Damaged(), MaxFacts)
	}
	size := len(readFile(t, filepath.Join(path, FactsFile)))

	_, err = d.Add(note("one too many", t0), nil)
	var rejected *Rejected
	if !errors.As(err, &rejected) || !strings.Contains(rejected.Why, "already holds 2000 facts") {
		t.Fatalf("adding fact %d = %v, want a Rejected that names the limit", MaxFacts+1, err)
	}
	if got := len(readFile(t, filepath.Join(path, FactsFile))); got != size {
		t.Error("a refused fact was written to the journal all the same")
	}
	if err := d.Retract(all[0].ID, fact.Retraction{At: t0, Reason: "making room"}); err != nil {
		t.Fatal(err)
	}
	mustAdd(t, d, note("fits after a retraction", t0), nil)
	if _, err := another(t, d).Add(note("one too many again", t0), nil); !errors.As(err, &rejected) {
		t.Errorf("another process added past the limit: %v", err)
	}
}

func TestInitApprovesThePolicyItWrites(t *testing.T) {
	d := newDir(t)
	p, why := d.Policy()
	if why != "" {
		t.Fatalf("the policy Init wrote is not in force: %s", why)
	}
	if p.String() != policy.Default().String() {
		t.Errorf("the policy in force is not the one Init wrote:\n%s", p)
	}
	text, approved, err := d.PolicyText()
	if err != nil || !approved || text != policy.Default().String() {
		t.Errorf("PolicyText() = %q, %v, %v; want the default policy, approved", text, approved, err)
	}
}

func TestTheApprovalsFileLivesUnderHoldfastStateAndNowhereElse(t *testing.T) {
	isolate(t)
	state, home := os.Getenv("HOLDFAST_STATE"), os.Getenv("HOME")
	d, err := Init(filepath.Join(t.TempDir(), DirName), policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	approvals := readFile(t, filepath.Join(state, "approved"))
	fields := strings.Fields(strings.TrimSpace(approvals))
	if len(fields) != 2 || len(fields[0]) != 64 || fields[1] != d.Path() {
		t.Errorf("the approvals file holds %q, want one line: a sha256 and %s", approvals, d.Path())
	}
	if got := names(t, home); len(got) != 0 {
		t.Errorf("with HOLDFAST_STATE set, the home folder gained %v", got)
	}
	if got := names(t, d.Path()); slices.Contains(got, "approved") {
		t.Error("the approval was written inside the store folder, where a repository could bring its own")
	}
}

func TestWithoutHoldfastStateApprovalsGoToTheUserConfigFolder(t *testing.T) {
	isolate(t)
	t.Setenv("HOLDFAST_STATE", "")
	config, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no config folder on this system: %v", err)
	}
	if !strings.HasPrefix(config, os.Getenv("HOME")) && !strings.HasPrefix(config, os.Getenv("XDG_CONFIG_HOME")) {
		t.Skipf("the config folder %s does not follow HOME here, so this test would write to the real one", config)
	}
	d, err := Init(filepath.Join(t.TempDir(), DirName), policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got := readFile(t, filepath.Join(config, "holdfast", "approved")); !strings.Contains(got, " "+d.Path()+"\n") {
		t.Errorf("the approvals file under %s holds %q, want a line for %s", config, got, d.Path())
	}
}

func TestAHandEditedPolicyIsNotInForceUntilItIsTrusted(t *testing.T) {
	d := newDir(t)
	edited := policy.Default().String() + "net public\ncmd git status\n"
	writeFile(t, filepath.Join(d.Path(), PolicyFile), edited)

	p, why := d.Policy()
	wantNote := "the policy file has changed and nobody has approved it on this machine, so only the defaults apply. " +
		"A person can read .holdfast/policy and approve it: holdfast trust"
	if why != wantNote {
		t.Errorf("note = %q\nwant   %q", why, wantNote)
	}
	if p.String() != policy.Default().String() {
		t.Errorf("an unapproved edit changed the policy in force:\n%s", p)
	}
	if err := p.Command([]string{"git", "status"}); err == nil {
		t.Error("a command from an unapproved policy is allowed")
	}
	text, approved, err := d.PolicyText()
	if err != nil || approved || text != edited {
		t.Errorf("PolicyText() = %q, %v, %v; want the file as edited, not approved", text, approved, err)
	}
	if _, why := another(t, d).Policy(); why == "" {
		t.Error("a second process treats the unapproved edit as approved")
	}

	if err := d.Trust(); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	p, why = d.Policy()
	if why != "" {
		t.Fatalf("after Trust the policy is still not in force: %s", why)
	}
	if err := p.Command([]string{"git", "status"}); err != nil {
		t.Errorf("after Trust the allowed command is refused: %v", err)
	}
	if len(p.Net) != 2 || p.Net[1].Class != "public" {
		t.Errorf("after Trust the net rules are %v, want loopback and public", p.Net)
	}
	if _, approved, _ := d.PolicyText(); !approved {
		t.Error("after Trust PolicyText still says not approved")
	}

	// Approval is of the exact bytes: one more edit and it is gone again.
	writeFile(t, filepath.Join(d.Path(), PolicyFile), edited+"# a comment only\n")
	if _, why := d.Policy(); why == "" {
		t.Error("a later change to the file kept the old approval")
	}
	// And changing it back to the approved bytes needs nothing.
	writeFile(t, filepath.Join(d.Path(), PolicyFile), edited)
	if _, why := d.Policy(); why != "" {
		t.Errorf("the file is byte for byte what was approved, yet: %s", why)
	}
}

func TestSetPolicyTextApprovesAndKeepsThePersonsOwnWords(t *testing.T) {
	d := newDir(t)
	text := "# our rules\n\nfiles . ../shared\n\n# the staging host\nnet loopback staging.example.com\ncmd systemctl is-active *\n"
	if err := d.SetPolicyText(text); err != nil {
		t.Fatalf("SetPolicyText: %v", err)
	}
	if got := readFile(t, filepath.Join(d.Path(), PolicyFile)); got != text {
		t.Errorf("the policy file holds %q, want the text as given", got)
	}
	p, why := d.Policy()
	if why != "" {
		t.Fatalf("a policy set through the store is not in force: %s", why)
	}
	if !slices.Equal(p.Files, []string{".", "../shared"}) || len(p.Net) != 2 || len(p.Cmds) != 1 {
		t.Errorf("the policy in force is %+v", p)
	}
	if _, why := another(t, d).Policy(); why != "" {
		t.Errorf("another process on this machine does not see the approval: %s", why)
	}
	want := []string{".gitattributes", ".gitignore", "evidence.jsonl", "facts.jsonl", "lock", "policy"}
	if got := names(t, d.Path()); !slices.Equal(got, want) {
		t.Errorf("after SetPolicyText the folder holds %v, want %v (no temporary file left behind)", got, want)
	}
	if info, err := os.Stat(filepath.Join(d.Path(), PolicyFile)); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("the policy file's mode is %v, want 0644 so a teammate can read it", info.Mode().Perm())
	}
}

func TestSetPolicyWritesAPolicyThatReadsBack(t *testing.T) {
	d := newDir(t)
	rule, err := policy.ParseNetRule("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	in := &policy.Policy{
		Files: []string{".", "a folder with spaces"},
		Net:   []policy.NetRule{{Class: "loopback"}, rule},
		Cmds:  [][]string{{"git", "status", "--short"}, {"echo", "two words", "**"}},
	}
	if err := d.SetPolicy(in); err != nil {
		t.Fatal(err)
	}
	p, why := d.Policy()
	if why != "" {
		t.Fatalf("not in force: %s", why)
	}
	if p.String() != in.String() {
		t.Errorf("the policy read back is\n%s\nwant\n%s", p, in)
	}
}

func TestSetPolicyTextRefusesAPolicyWithAMistake(t *testing.T) {
	d := newDir(t)
	before := readFile(t, filepath.Join(d.Path(), PolicyFile))
	for _, text := range []string{"allow everything\n", "cmd *\n", "net\n", "files \"unclosed\n"} {
		if err := d.SetPolicyText(text); err == nil {
			t.Errorf("SetPolicyText(%q) succeeded", text)
		}
	}
	if got := readFile(t, filepath.Join(d.Path(), PolicyFile)); got != before {
		t.Errorf("a refused policy changed the file: %q", got)
	}
	if _, why := d.Policy(); why != "" {
		t.Errorf("a refused policy cost the old one its approval: %s", why)
	}
}

// A policy file that cannot be understood must not fall back to the
// defaults: nobody can say what the person meant to forbid.
func TestAPolicyFileWithAMistakeAllowsNothing(t *testing.T) {
	d := newDir(t)
	writeFile(t, filepath.Join(d.Path(), PolicyFile), "files .\nnet loopback\nallow everything\n")
	p, why := d.Policy()
	if len(p.Files) != 0 || len(p.Net) != 0 || len(p.Cmds) != 0 {
		t.Errorf("a policy file with a mistake still allows something: %+v", p)
	}
	if !strings.HasPrefix(why, "the policy file has a mistake (policy line 3: ") || !strings.HasSuffix(why, "so no check may touch anything until it is fixed") {
		t.Errorf("note = %q", why)
	}
	if _, approved, err := d.PolicyText(); err != nil || approved {
		t.Errorf("PolicyText says approved = %v, err = %v for a broken file", approved, err)
	}
	err := d.Trust()
	if err == nil || !strings.HasPrefix(err.Error(), "the policy file has a mistake: ") {
		t.Errorf("Trust of a broken policy file = %v, want a refusal that names the mistake", err)
	}
	if p, _ := d.Policy(); len(p.Files) != 0 || len(p.Net) != 0 {
		t.Errorf("after a refused Trust the broken policy allows something: %+v", p)
	}
}

func TestAPolicyFileThatCannotBeReadAllowsNothing(t *testing.T) {
	d := newDir(t)
	path := filepath.Join(d.Path(), PolicyFile)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A folder where the file should be cannot be read as a file, whoever
	// runs the tests.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	p, why := d.Policy()
	if len(p.Files) != 0 || len(p.Net) != 0 || len(p.Cmds) != 0 {
		t.Errorf("an unreadable policy still allows something: %+v", p)
	}
	if want := "the policy file cannot be read, so no check may touch anything until it can"; why != want {
		t.Errorf("note = %q, want %q", why, want)
	}
	if err := d.Trust(); err == nil {
		t.Error("Trust of an unreadable policy file succeeded")
	}
}

func TestAStoreWithNoPolicyFileRunsOnItsBasePolicy(t *testing.T) {
	isolate(t)
	base := &policy.Policy{Net: []policy.NetRule{{Class: "loopback"}}}
	d, err := Init(filepath.Join(t.TempDir(), DirName), base)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got := readFile(t, filepath.Join(d.Path(), PolicyFile)); got != base.String() {
		t.Errorf("Init wrote %q, want the base policy it was given", got)
	}
	if err := os.Remove(filepath.Join(d.Path(), PolicyFile)); err != nil {
		t.Fatal(err)
	}
	p, why := d.Policy()
	if p != base || why != "" {
		t.Errorf("Policy() = %v, %q; want the base policy and no note", p, why)
	}
	text, approved, err := d.PolicyText()
	if err != nil || !approved || text != base.String() {
		t.Errorf("PolicyText() = %q, %v, %v; want the base policy", text, approved, err)
	}
	if err := d.Trust(); err == nil {
		t.Error("Trust with no policy file to approve succeeded")
	}
}

func TestApprovalIsPerMachine(t *testing.T) {
	d := newDir(t)
	if err := d.SetPolicyText(policy.Default().String() + "net public\n"); err != nil {
		t.Fatal(err)
	}
	if _, why := d.Policy(); why != "" {
		t.Fatalf("not approved where it was written: %s", why)
	}
	here := os.Getenv("HOLDFAST_STATE")
	// The same project folder seen from a machine that has approved nothing.
	t.Setenv("HOLDFAST_STATE", t.TempDir())
	if _, why := d.Policy(); why == "" {
		t.Error("a machine that never approved the policy treats it as approved")
	}
	t.Setenv("HOLDFAST_STATE", here)
	if _, why := d.Policy(); why != "" {
		t.Errorf("back on the first machine: %s", why)
	}
}

func TestApprovalIsPerStoreAndOneDoesNotDisturbAnother(t *testing.T) {
	isolate(t)
	open := func() *Dir {
		d, err := Init(filepath.Join(t.TempDir(), DirName), policy.Default())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		return d
	}
	a, b := open(), open()
	// The same words in both, approved in only one.
	text := policy.Default().String() + "net public\n"
	if err := a.SetPolicyText(text); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.Path(), PolicyFile), text)
	if _, why := b.Policy(); why == "" {
		t.Error("approving a policy in one project approved the same words in another")
	}
	if err := b.Trust(); err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]*Dir{"first": a, "second": b} {
		if _, why := d.Policy(); why != "" {
			t.Errorf("the %s store lost its approval: %s", name, why)
		}
	}
	// One line per store: an approval replaces the store's old one.
	for range 3 {
		if err := a.Trust(); err != nil {
			t.Fatal(err)
		}
	}
	approvals := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(os.Getenv("HOLDFAST_STATE"), "approved"))), "\n")
	if len(approvals) != 2 {
		t.Errorf("the approvals file holds %d lines for 2 stores:\n%s", len(approvals), strings.Join(approvals, "\n"))
	}
}

// The approval names the folder. A copy of the project elsewhere (or the same
// repository cloned twice) has to be approved for itself.
func TestACopyOfAStoreElsewhereIsNotApproved(t *testing.T) {
	d := newDir(t)
	text := policy.Default().String() + "cmd make test\n"
	if err := d.SetPolicyText(text); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), DirName)
	if err := os.CopyFS(copyPath, os.DirFS(d.Path())); err != nil {
		t.Fatal(err)
	}
	c, err := Open(copyPath, policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p, why := c.Policy()
	if why == "" {
		t.Fatal("a copy of an approved store is approved without anyone looking at it")
	}
	if len(p.Cmds) != 0 {
		t.Errorf("the copy's unapproved commands are in force: %v", p.Cmds)
	}
}

func TestManyGoroutinesShareOneStore(t *testing.T) {
	for _, mode := range []string{"one handle", "a handle each"} {
		t.Run(mode, func(t *testing.T) {
			d := newDir(t)
			const workers, each = 8, 12
			check := parse(t, "file go.mod")
			handles := make([]*Dir, workers)
			for w := range handles {
				handles[w] = d
				if mode == "a handle each" {
					handles[w] = another(t, d)
				}
			}
			done := make(chan error, workers)
			for w := range workers {
				go func() {
					h := handles[w]
					for i := range each {
						first := evidence(fact.Holds, at(time.Duration(i)*time.Second))
						f, err := h.Add(&fact.Fact{Claim: fmt.Sprintf("worker %d fact %d", w, i), Check: check.Clone(), By: "worker", Created: t0}, &first)
						if err != nil {
							done <- fmt.Errorf("worker %d: Add: %w", w, err)
							return
						}
						if err := h.Record([]Proof{{ID: f.ID, Ev: evidence(fact.Broken, at(time.Hour))}}); err != nil {
							done <- fmt.Errorf("worker %d: Record: %w", w, err)
							return
						}
						got, err := h.Get(f.ID)
						if err != nil || got.Last == nil || got.Last.Status != fact.Broken {
							done <- fmt.Errorf("worker %d: its own fact reads back as %+v, %v", w, got, err)
							return
						}
						if _, err := h.Facts(); err != nil {
							done <- fmt.Errorf("worker %d: Facts: %w", w, err)
							return
						}
						h.Policy()
						h.Damaged()
					}
					done <- nil
				}()
			}
			for range workers {
				if err := <-done; err != nil {
					t.Error(err)
				}
			}
			wholeAndComplete(t, d.Path(), workers, each)
		})
	}
}

// wholeAndComplete checks a store that several writers shared: every fact is
// there exactly once, with its evidence, and no journal line is torn.
func wholeAndComplete(t *testing.T, path string, workers, each int) {
	t.Helper()
	d, err := Open(path, policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	all := mustFacts(t, d)
	if len(all) != workers*each {
		t.Errorf("the store holds %d facts, want %d", len(all), workers*each)
	}
	ids, claims := map[string]int{}, map[string]int{}
	for _, f := range all {
		ids[f.ID]++
		claims[f.Claim]++
		if f.Last == nil || f.Last.Status != fact.Broken || statuses(f.History) != "holds broken" {
			t.Errorf("%s (%s) has last evidence %+v and history %q, want holds then broken", f.ID, f.Claim, f.Last, statuses(f.History))
		}
	}
	for id, n := range ids {
		if n != 1 {
			t.Errorf("the id %s names %d facts", id, n)
		}
	}
	for w := range workers {
		for i := range each {
			if claim := fmt.Sprintf("worker %d fact %d", w, i); claims[claim] != 1 {
				t.Errorf("%q is stored %d times, want exactly once", claim, claims[claim])
			}
		}
	}
	if got := d.Damaged(); got != 0 {
		t.Errorf("Damaged() = %d, want 0", got)
	}
	// lines fails the test on any line that is not one whole event.
	adds := 0
	for _, e := range lines(t, filepath.Join(path, FactsFile)) {
		if e.Op == "add" {
			adds++
		}
	}
	if adds != workers*each {
		t.Errorf("facts.jsonl holds %d add lines, want %d", adds, workers*each)
	}
	if got := len(lines(t, filepath.Join(path, EvidenceFile))); got != 2*workers*each {
		t.Errorf("evidence.jsonl holds %d lines, want %d", got, 2*workers*each)
	}
}

// A project that never widened its policy carries the default file. On a
// machine that has approved nothing, that file is in force as it stands:
// approving it would allow nothing more, so there is nothing to ask a person.
func TestAPolicyThatSaysWhatTheDefaultsSayNeedsNoApproval(t *testing.T) {
	d := newDir(t)
	// The same project folder seen from a machine that has approved nothing.
	t.Setenv("HOLDFAST_STATE", t.TempDir())
	file := filepath.Join(d.Path(), PolicyFile)
	inForce := func(when string, want bool) {
		t.Helper()
		p, why := d.Policy()
		_, approved, err := d.PolicyText()
		if err != nil {
			t.Fatal(err)
		}
		if got := why == ""; got != want || approved != want || d.Unwidened() != want {
			t.Errorf("%s: in force %v (%q), PolicyText says %v, Unwidened %v; want %v", when, got, why, approved, d.Unwidened(), want)
		}
		if p.String() != policy.Default().String() {
			t.Errorf("%s: the rules in force are\n%s\nwant the defaults", when, p)
		}
	}
	inForce("the default file", true)

	// Comments and spacing allow nothing, so they change nothing.
	writeFile(t, file, "# our team's policy\n\nfiles .\n  net   loopback\n")
	inForce("the defaults in other words", true)

	// One line more is a policy somebody has to read first.
	writeFile(t, file, policy.Default().String()+"net public\n")
	inForce("a wider file", false)
	writeFile(t, file, policy.Default().String()+"cmd make test\n")
	inForce("a file that allows a command", false)

	// Once a person approves it, it is in force because of that.
	if err := d.Trust(); err != nil {
		t.Fatal(err)
	}
	if _, why := d.Policy(); why != "" || d.Unwidened() {
		t.Errorf("after approval: %q, Unwidened %v; want it in force as approved", why, d.Unwidened())
	}
}

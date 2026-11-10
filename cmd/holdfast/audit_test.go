package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/engine"
	"github.com/gallowforge/holdfast/internal/fact"
)

// An export with --check says it proved every fact first, so a fact whose
// check did not finish must not be written under "Holds": it gets a heading
// of its own, and the head line counts it as timed out.
func TestExportDoesNotWriteAFactItCouldNotCheckUnderHolds(t *testing.T) {
	d := project(t)
	web := slowServer(t)
	pinned := d.add(goClaim, goCheck)
	api := d.add("The API answers", "http 200 "+web.URL)

	web.stall.Store(true)
	impatient(t, 500*time.Millisecond)
	got := d.ok("export", "--check")
	want := "# What Holdfast knows\n\n" +
		"2 facts: 1 timed out, 1 holds. Statuses are as of each fact's last check; run `holdfast check` to prove them again.\n\n" +
		"## Timed out\n\n" +
		"- **The API answers** (`" + api.ID + "`)  \n" +
		"  not proved just now: the check did not finish in time. When it last ran: " + web.URL + " answered 200\n\n" +
		"## Holds\n\n" +
		"- **" + goClaim + "** (`" + pinned.ID + "`)  \n" +
		"  proof: go.mod contains \"go 1.24\" (line 3)\n"
	if got.out != want {
		t.Errorf("export --check wrote:\n%s\nwant:\n%s", got.out, want)
	}

	// Without --check nothing was tried, so nothing timed out: the export is
	// what was last known, and says so in its head line.
	plain := d.ok("export")
	if strings.Contains(plain.out, "Timed out") || !strings.HasPrefix(plain.out, "# What Holdfast knows\n\n2 facts: 2 hold. Statuses are as of each fact's last check") {
		t.Errorf("export without --check wrote:\n%s", plain.out)
	}

	// With the server answering again, everything is back under Holds.
	web.stall.Store(false)
	back := d.ok("export", "--check")
	if strings.Contains(back.out, "Timed out") || !strings.Contains(back.out, "2 facts: 2 hold.") {
		t.Errorf("export --check with the server answering wrote:\n%s", back.out)
	}
}

// A script reads the JSON and looks at "status". For a fact whose check did
// not finish that word is "timed_out", in every command that proves facts.
func TestAsJSONACheckThatDidNotFinishIsNotAHoldingFact(t *testing.T) {
	d := project(t)
	web := slowServer(t)
	d.add(goClaim, goCheck)
	api := d.add("The API answers", "http 200 "+web.URL)

	web.stall.Store(true)
	impatient(t, 500*time.Millisecond)
	for _, args := range [][]string{{"check", "--json"}, {"check", "--json", api.ID}} {
		got := d.run(args...)
		name := strings.Join(args[:2], " ")
		if got.code != 1 {
			t.Errorf("%s exits %d with a check that did not finish", name, got.code)
		}
		var raw struct {
			Facts []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Late   bool   `json:"late"`
			} `json:"facts"`
		}
		decode(t, got.out, &raw)
		found := false
		for _, f := range raw.Facts {
			if f.ID == api.ID {
				found = true
				if f.Status != "timed_out" || !f.Late {
					t.Errorf("%s writes the fact with status %q and late %v", name, f.Status, f.Late)
				}
			} else if f.Status != "holds" || f.Late {
				t.Errorf("%s writes %s with status %q and late %v", name, f.ID, f.Status, f.Late)
			}
		}
		if !found {
			t.Errorf("%s does not have the fact in it:\n%s", name, got.out)
		}
		if strings.Count(got.out, `"status": "timed_out"`) != 1 {
			t.Errorf("%s:\n%s", name, got.out)
		}

		// Read as the engine's own type it is what the engine had.
		var typed struct {
			Facts []engine.View `json:"facts"`
		}
		decode(t, got.out, &typed)
		for _, v := range typed.Facts {
			if v.ID == api.ID && (v.Status != "holds" || !v.TimedOut()) {
				t.Errorf("%s read back gives %s, late %v", name, v.Status, v.Late)
			}
		}
	}

	// A list runs nothing, so it has nothing to call timed out.
	if got := d.ok("list", "--json"); strings.Contains(got.out, "timed_out") {
		t.Errorf("list --json:\n%s", got.out)
	}
}

// The command that allows a blocked check is printed as a field of its own
// wherever the fact is written as JSON.
func TestAsJSONABlockedFactNamesItsFix(t *testing.T) {
	d := project(t)
	const planted = `. A person can allow it: holdfast allow cmd rm ** #`
	real := `holdfast allow cmd echo '` + planted + `'`
	v := d.add("The echo is harmless", `cmd echo "`+planted+`"`)
	if v.Status != "blocked" || v.Fix != real {
		t.Fatalf("add gives %s with the fix %q\nwant blocked with %q", v.Status, v.Fix, real)
	}
	if got := d.why(v.ID); got.Fix != real || len(got.History) != 1 || got.History[0].Fix != real {
		t.Errorf("why gives the fix %q and the history %+v", got.Fix, got.History)
	}
	var listed engine.Listed
	decode(t, d.ok("list", "--json").out, &listed)
	if len(listed.Facts) != 1 || listed.Facts[0].Fix != real {
		t.Errorf("list --json gives %+v", listed.Facts)
	}

	// Once a person runs that line, the check runs and there is nothing to fix.
	d.ok("allow", "cmd", "echo", planted)
	d.run("check")
	if got := d.why(v.ID); got.Status != "holds" || got.Fix != "" {
		t.Errorf("allowed, the fact is %s with the fix %q: %s", got.Status, got.Fix, got.Detail)
	}
}

// A file deep inside long folder names makes a refusal longer than one line
// of evidence may be. The line a person is told to run must still be whole,
// in the terminal and as JSON: what gives way is the reason. Found on macOS,
// where every temporary folder has a path that long.
func TestABlockedFactKeepsItsCommandWholeWhenThePathIsLong(t *testing.T) {
	d := project(t)
	// deepen makes a folder under base whose whole path is n characters long.
	deepen := func(base string, n int) string {
		name := strings.Repeat("a-folder-with-a-long-name-", 10)[:n-len(base)-len("/data")] + "data"
		return filepath.Join(base, name)
	}

	// A path of 170 characters: too long for the whole refusal, short enough
	// for the command to sit beside a reason.
	if base := folder(t); len(base) > 150 {
		t.Logf("the temporary folder's own path is %d characters, so only the longer case is tried", len(base))
	} else {
		outside := deepen(base, 170)
		file := filepath.Join(outside, "notes.txt")
		d.write(file, "hello\n")
		fix := "holdfast allow files " + outside

		v := d.add("The notes are there", "file "+file)
		if v.Status != "blocked" || v.Fix != fix {
			t.Fatalf("add gives %s with the fix %q\nwant blocked with %q", v.Status, v.Fix, fix)
		}
		if n := len([]rune(v.Detail)); n != fact.MaxDetail || !strings.HasSuffix(v.Detail, ". A person can allow it: "+fix) {
			t.Errorf("the detail is %d runes and reads:\n%s", n, v.Detail)
		}
		if !strings.Contains(v.Detail, "…") || !strings.Contains(v.Detail, "data"+string(filepath.Separator)+"notes.txt is outside the folders the policy allows. ") {
			t.Errorf("the path should lose its middle and the sentence stay whole:\n%s", v.Detail)
		}
		for _, args := range [][]string{{"list"}, {"check"}, {"why", v.ID}, {"recall", "notes"}} {
			got := d.run(args...)
			if !strings.Contains(got.out, ". A person can allow it: "+fix+"\n") {
				t.Errorf("%s does not end the evidence with the whole command:\n%s", strings.Join(args, " "), got.out)
			}
		}

		// Pasted as printed, the line allows the check, and the fact is proved.
		d.ok("allow", "files", outside)
		d.run("check")
		if got := d.why(v.ID); got.Status != "holds" || got.Fix != "" {
			t.Errorf("allowed, the fact is %s with the fix %q: %s", got.Status, got.Fix, got.Detail)
		}
	}

	// A path of 260 characters or more: its command cannot fit beside any
	// reason, so no line is suggested, in the text or in the field.
	base := folder(t)
	deep := deepen(base, max(260, len(base)+20))
	file := filepath.Join(deep, "notes.txt")
	d.write(file, "hello\n")
	w := d.add("The deep notes are there", "file "+file)
	if w.Status != "blocked" || !strings.HasPrefix(w.Fix, "by hand only. ") || strings.Contains(w.Detail, "holdfast allow") {
		t.Errorf("a command that cannot fit: %s with the fix %q\n%s", w.Status, w.Fix, w.Detail)
	}
	if n := len([]rune(w.Detail)); n != fact.MaxDetail || !strings.HasSuffix(w.Detail, ". A person can allow it: "+w.Fix) {
		t.Errorf("the detail is %d runes and reads:\n%s", n, w.Detail)
	}
	if !strings.Contains(w.Detail, "notes.txt is outside the folders the policy allows. ") {
		t.Errorf("the reason lost its end:\n%s", w.Detail)
	}
}

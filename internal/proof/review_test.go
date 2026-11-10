package proof

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
)

// A "matches" check used to hand the whole file to the regexp package, which
// cannot be stopped: a heavy pattern on a large file ran for as long as it
// liked, whatever the check's time limit said.
func TestMatchesStopsWhenItsTimeIsUp(t *testing.T) {
	w := machine()
	w.Write("words.txt", strings.Repeat("the quick brown fox jumps over the lazy dog\n", 2<<20/44), noon)
	// Under the limit on how large a pattern may be, and still seconds of
	// work on two mebibytes of lower-case words.
	c := parse(t, "matches words.txt '(?:[a-z ]{1,30}){1,15}Q'")

	ctx, cancel := context.WithTimeout(bg, 50*time.Millisecond)
	defer cancel()
	began := time.Now()
	ok, detail, err := observe(ctx, w, open(), c, Timeout)
	took := time.Since(began)
	if ok || err == nil || err.Error() != "searching words.txt took too long" {
		t.Errorf("observe = %v, %q, %v; want it to say that the search took too long", ok, detail, err)
	}
	if took > 2*time.Second {
		t.Errorf("the search went on for %v after being given 50ms", took)
	}

	// A caller that has stopped waiting is not kept waiting either, and
	// nothing is concluded about the fact.
	gone, stop := context.WithCancel(bg)
	stop()
	began = time.Now()
	ev := Run(gone, w, open(), c)
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("the search went on for %v for a caller that had gone", took)
	}
	if ev.Status != fact.Blocked || ev.Detail != "could not check: the check was stopped before it finished" {
		t.Errorf("for a caller that had gone the evidence is %s, %q", ev.Status, ev.Detail)
	}
}

// The reader a pattern is fed from stops within a page of text once the
// context has ended, however long the file is.
func TestThePatternsReaderRunsDryWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(bg)
	p := &patient{ctx: ctx, r: bytes.NewReader(bytes.Repeat([]byte("a"), 1<<20))}
	for i := 0; i < 5000; i++ {
		if _, _, err := p.ReadRune(); err != nil {
			t.Fatalf("the reader stopped at character %d of a file it was still allowed to read: %v", i, err)
		}
	}
	cancel()
	for i := 0; ; i++ {
		if _, _, err := p.ReadRune(); err != nil {
			break
		}
		if i > 1024 {
			t.Fatal("the reader was still reading 1,024 characters after the context ended")
		}
	}
}

// A pattern too large to run quickly never reaches the file, even when the
// check was not written through ParseCheck.
func TestAPatternThatWouldBeSlowIsNotRun(t *testing.T) {
	c := &fact.Check{Kind: "matches", Path: "Makefile", Regex: "(?:[a-z ]{1,30}){1,30}Q"}
	ev := Run(bg, machine(), open(), c)
	if want := "could not check: the pattern is too complicated to run quickly; make it simpler"; ev.Status != fact.Blocked || ev.Detail != want {
		t.Errorf("evidence is %s, %q; want blocked, %q", ev.Status, ev.Detail, want)
	}
}

// Being read through a reader changes nothing about what a pattern finds or
// where: the answer and the line are the ones a search of the whole file
// gives.
func TestMatchesFindsWhatASearchOfTheWholeFileFinds(t *testing.T) {
	long := strings.Repeat("x", 70<<10) // one line longer than 64 KiB
	files := map[string]string{
		"plain.txt":  "alpha\nbeta\ngamma\n",
		"crlf.txt":   "alpha\r\nbeta\r\ngamma\r\n",
		"noeol.txt":  "alpha\nbeta",
		"empty.txt":  "",
		"long.txt":   "first\n" + long + "needle\nlast\n",
		"deep.txt":   strings.Repeat("filler line\n", 3000) + "needle here\n",
		"binary.bin": "ok\n\xff\xfe\x00 needle \xc3\n",
		"words.txt":  "naïve café\nrésumé\n",
	}
	patterns := []string{
		"^beta$", "beta$", `beta\r$`, "^gamma", `\Aalpha`, `\Abeta`, `gamma\n\z`, `beta\z`, "^$",
		"(?s)alpha.*gamma", "alpha.*gamma", "(?-m)^beta", `a\nb`, `\bbeta\b`, "(?i)BETA",
		"needle", "^needle", "needle$", `x{100}needle`, `^last$`, `needle here`, `\x00 needle`, "caf.$", "^résumé$", `\pL+é$`,
	}
	w := machine()
	for name, text := range files {
		w.Write(name, text, noon)
	}
	for name, text := range files {
		for _, expr := range patterns {
			re, err := fact.Pattern(expr)
			if err != nil {
				t.Fatalf("Pattern(%q): %v", expr, err)
			}
			want := fmt.Sprintf("%s does not match %s", name, quote(expr))
			if loc := re.FindStringIndex(text); loc != nil {
				want = fmt.Sprintf("%s matches %s (line %d)", name, quote(expr), 1+strings.Count(text[:loc[0]], "\n"))
			}
			c := &fact.Check{Kind: "matches", Path: name, Regex: expr}
			_, got, err := observe(bg, w, open(), c, Timeout)
			if err != nil || got != want {
				t.Errorf("matches %s %q: %q, %v; want %q", name, expr, got, err, want)
			}
		}
	}
}

// What a person reads about a refused command is its evidence, and the line
// in it is there to be pasted. A command the pasted line would not allow
// exactly gets the reason instead, whole, and no line.
func TestARefusedCommandOnlyGetsALineThatIsSafeToPaste(t *testing.T) {
	w, p := machine(), open()
	const (
		refused = "the policy does not allow the command: "
		wild    = `. A person can allow it: by hand only. The policy reads its argument %s as "anything", so the rule must be written into the policy file`
		long    = ". A person can allow it: by hand only. The command is too long to suggest a line for, so the rule must be written into the policy file"
	)
	folders := strings.TrimSpace(strings.Repeat("build/tmp ", 30))
	for line, want := range map[string]string{
		"cmd echo *":             refused + "echo *" + fmt.Sprintf(wild, "*"),
		"says ok make test **":   refused + "make test **" + fmt.Sprintf(wild, "**"),
		"cmd rm -r " + folders:   refused + fact.CleanLine("rm -r "+folders, 120) + long,
		"cmd rm -r build/tmp":    refused + "rm -r build/tmp. A person can allow it: holdfast allow cmd rm -r build/tmp",
		"cmd ls *.go":            refused + "ls *.go. A person can allow it: holdfast allow cmd ls '*.go'",
		"cmd systemctl status *": refused + "systemctl status *" + fmt.Sprintf(wild, "*"),
	} {
		blockedBoth(t, w, p, *parse(t, line), want)
		if strings.HasSuffix(want, "…") || len([]rune(want)) > fact.MaxDetail {
			t.Errorf("%s: the test expects evidence that is cut short: %s", line, want)
		}
	}
}

package policy

import (
	"fmt"
	"strings"
	"testing"
)

// A policy file is read by the person who approves it. A mark that changes
// the order text is drawn in could make a line show as something other than
// what it allows, so a line with one in it is refused like any other line
// that could hide what it says.
func TestParseRefusesTheMarksThatReorderText(t *testing.T) {
	marks := map[string]string{
		"a left-to-right embedding": "\u202a",
		"a right-to-left embedding": "\u202b",
		"a pop directional format":  "\u202c",
		"a left-to-right override":  "\u202d",
		"a right-to-left override":  "\u202e",
		"a left-to-right isolate":   "\u2066",
		"a right-to-left isolate":   "\u2067",
		"a first-strong isolate":    "\u2068",
		"a pop directional isolate": "\u2069",
	}
	lines := map[string]string{
		"a files line": "files docs%s other",
		"a net line":   "net example.com%s",
		"a cmd line":   "cmd echo %shello",
		"a comment":    "# only the project%s and nothing else",
		"quoted text":  `cmd echo "a%sb"`,
	}
	for what, m := range marks {
		for where, line := range lines {
			text := "files .\n" + fmt.Sprintf(line, m) + "\nnet loopback\n"
			p, err := Parse(text)
			if want := "policy line 2: " + controlMistake; p != nil || err == nil || err.Error() != want {
				t.Errorf("%s in %s: Parse = %v, %v\nwant %s", what, where, p, err, want)
			}
		}
	}

	// Their ordinary neighbours are still allowed in a comment.
	if _, err := Parse("files .\n# 10\u202fkm of notes\nnet loopback\n"); err != nil {
		t.Errorf("a narrow no-break space in a comment: %v", err)
	}
}

// Evidence is one line of limited length, and the line ends with the command
// a person can paste. A long path must shorten the reason and never the
// command: a command that lost its end is a different command.
func TestARefusalThatIsTooLongKeepsItsCommandWhole(t *testing.T) {
	count := func(s string) int { return len([]rune(s)) }

	// One that fits is left exactly as it was.
	short := Outside("/srv/data/notes.txt", "/srv/data")
	if got := short.Within(300); got != short {
		t.Errorf("a refusal that fits was rewritten: %q", got.Error())
	}
	if got, want := short.Error(), "/srv/data/notes.txt is outside the folders the policy allows. A person can allow it: holdfast allow files /srv/data"; got != want {
		t.Errorf("the refusal reads %q\nwant %q", got, want)
	}

	// One with no command has nothing to protect; whoever shows it cuts it.
	bare := &Blocked{Why: strings.Repeat("x", 400)}
	if got := bare.Within(300); got != bare {
		t.Errorf("a refusal with no command was rewritten")
	}

	// A long folder: the reason loses its middle and the command stays whole.
	folder := "/srv/" + strings.Repeat("a-folder-with-a-long-name/", 5) + "data"
	long := Outside(folder+"/notes.txt", folder)
	if count(long.Error()) <= 300 {
		t.Fatalf("the long refusal is only %d runes; the test needs it over 300", count(long.Error()))
	}
	got := long.Within(300)
	fix := "holdfast allow files " + folder
	if got.Fix != fix || !strings.HasSuffix(got.Error(), ". A person can allow it: "+fix) {
		t.Errorf("the command was not kept whole:\n%s", got.Error())
	}
	if n := count(got.Error()); n != 300 {
		t.Errorf("the refusal is %d runes, want exactly 300", n)
	}
	if !strings.HasPrefix(got.Why, "/srv/a-folder-with-a-long-name/") || !strings.HasSuffix(got.Why, "-long-name/data/notes.txt is outside the folders the policy allows") || strings.Count(got.Why, "…") != 1 {
		t.Errorf("the path should lose its middle and the sentence stay whole: %q", got.Why)
	}
	if long.Fix != fix || strings.Contains(long.Why, "…") {
		t.Errorf("Within changed the refusal it was given: %+v", long)
	}

	// A folder so long that its command cannot sit beside any reason: no
	// command is suggested, and the reason is still told.
	deep := "/srv/" + strings.Repeat("a-folder-with-a-long-name/", 9) + "data"
	got = Outside(deep+"/notes.txt", deep).Within(300)
	if !strings.HasPrefix(got.Fix, "by hand only. ") || strings.Contains(got.Error(), "holdfast allow") {
		t.Errorf("a command that cannot fit was still suggested:\n%s", got.Error())
	}
	if n := count(got.Error()); n > 300 {
		t.Errorf("the refusal is %d runes, want at most 300", n)
	}
	if !strings.HasPrefix(got.Why, "/srv/a-folder-with-a-long-name/") || !strings.HasSuffix(got.Why, "/data/notes.txt is outside the folders the policy allows") {
		t.Errorf("the reason lost one of its ends: %q", got.Why)
	}

	// The cut counts characters, not bytes.
	wide := "/srv/" + strings.Repeat("éèêë-notes/", 30) + "data"
	got = Outside(wide+"/notes.txt", "/srv").Within(300)
	if n := count(got.Error()); n != 300 || got.Fix != "holdfast allow files /srv" || !strings.HasSuffix(got.Error(), got.Fix) {
		t.Errorf("a path of accented letters: %d runes, fix %q", n, got.Fix)
	}
}

// The word that gives way is the long one, the path or the address, so the
// sentence around it is never cut.
func TestShorten(t *testing.T) {
	host := strings.Repeat("a-very-long-label.", 6) + "example.com"
	for _, tc := range []struct {
		name, in string
		n        int
		want     string
	}{
		{"one that fits", "notes.txt is outside the folders the policy allows", 80, "notes.txt is outside the folders the policy allows"},
		{"a path at the start", "/srv/projects/atlas/data/notes.txt is outside the folders the policy allows", 70, "/srv/projects/…data/notes.txt is outside the folders the policy allows"},
		{"an address at the end", "the policy does not allow connections to " + host, 70, "the policy does not allow connections to a-very-long-la…el.example.com"},
		{"a path with spaces in it", "/srv/My Project Files/atlas-data-and-notes/notes.txt is outside the folders the policy allows", 85, "/srv/My Project Files/atlas-d…otes/notes.txt is outside the folders the policy allows"},
		{"no word long enough", "one two three four five six seven eight nine ten", 21, "one two th…t nine ten"},
		{"a word that would be left too short", "/srv/projects/atlas is outside the folders the policy allows", 50, "/srv/projects/atlas is o…folders the policy allows"},
	} {
		got := shorten(tc.in, tc.n)
		if got != tc.want {
			t.Errorf("%s: shorten(%q, %d) =\n%q, want\n%q", tc.name, tc.in, tc.n, got, tc.want)
		}
		if n, was := len([]rune(got)), len([]rune(tc.in)); n > tc.n || (was > tc.n && n != tc.n) {
			t.Errorf("%s: %d runes, want %d and no fewer than it must lose", tc.name, n, tc.n)
		}
	}
}

func TestMiddle(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"abcdefghij", 10, "abcdefghij"},
		{"abcdefghij", 12, "abcdefghij"},
		{"abcdefghij", 9, "abcd…ghij"},
		{"abcdefghij", 6, "ab…hij"},
		{"abcdefghij", 2, "…j"},
		{"abcdefghij", 1, "…"},
		{"abcdefghij", 0, ""},
		{"abcdefghij", -3, ""},
		{"", 5, ""},
	} {
		if got := middle(tc.in, tc.n); got != tc.want {
			t.Errorf("middle(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

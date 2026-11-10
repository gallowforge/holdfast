package policy

import (
	"fmt"
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

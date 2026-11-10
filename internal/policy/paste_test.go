package policy

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/gallowforge/holdfast/internal/fact"
)

// hostile are words a check could carry that mean something to a shell.
var hostile = []string{
	"", " ", "two words", "a  b", "tab\there", "line\nbreak",
	"$(id)", "`id`", "$HOME", "${HOME}", "a;id", "a&id", "a|id", "a>b", "a<b", "(a)", "{a,b}",
	"*", "?", "[ab]", "~", "~/notes", "~root", "~+", "~-", "a ~", "~/my notes", "=ls", "a=b", "#x", "!!", "a!b",
	"it's", `say "hi"`, `back\slash`, `'`, `''`, `'\''`, `\`, "-n", "-e", "--", "%s", "ünïcödé", "a\u00a0b",
}

func TestShellWord(t *testing.T) {
	tests := []struct{ in, want string }{
		{"notes", "notes"},
		{".", "."},
		{"sub/dir", "sub/dir"},
		{"/var/log", "/var/log"},
		{"~", "~"},
		{"~/notes", "~/notes"},
		{"example.com", "example.com"},
		{"127.0.0.1", "127.0.0.1"},
		{"fe80::1", "fe80::1"},
		{"--format=json", "--format=json"},
		{"user@host", "user@host"},
		{"ünïcödé", "ünïcödé"},
		{"", "''"},
		{"my notes", "'my notes'"},
		{"~/my notes", "'~/my notes'"},
		{"a ~", "'a ~'"},
		{"~root", "'~root'"}, // a shell would read another user's home
		{"~+", "'~+'"},       // and here the folder it is in
		{"=ls", "'=ls'"},     // zsh reads this as the path of ls
		{"$(id)", "'$(id)'"},
		{"`id`", "'`id`'"},
		{"a;id", "'a;id'"},
		{"*", "'*'"},
		{"#x", "'#x'"},
		{"it's", `'it'\''s'`},
		{`say "hi"`, `'say "hi"'`},
		{`back\slash`, `'back\slash'`},
	}
	for _, tt := range tests {
		if got := shellWord(tt.in); got != tt.want {
			t.Errorf("shellWord(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

// The proof that matters: a real shell reads every suggested word back as
// exactly that word, and runs nothing.
func TestShellWordInARealShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	home := t.TempDir()
	for _, w := range hostile {
		cmd := exec.Command(sh, "-c", `printf '%s\n' "$#"; printf '%s' "$1"`+"\n", "sh")
		// The word is placed where a pasted suggestion would have it: as text
		// of the command line, read by the shell.
		cmd.Args[2] = "set -- " + shellWord(w) + "\n" + cmd.Args[2]
		cmd.Env = []string{"HOME=" + home, "PATH=/nonexistent"}
		cmd.Dir = home
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("the shell could not read %s: %v: %s", shellWord(w), err, out)
			continue
		}
		want := w
		if strings.HasPrefix(shellWord(w), "~") {
			// Left for the shell to expand, to the same folder Holdfast would use.
			want = home + w[1:]
		}
		if string(out) != "1\n"+want {
			t.Errorf("the shell read %s as %q, want one word %q", shellWord(w), out, want)
		}
	}
}

func TestOutside(t *testing.T) {
	tests := []struct{ path, folder, want string }{
		{"notes/todo.md", "notes", "notes/todo.md is outside the folders the policy allows. A person can allow it: holdfast allow files notes"},
		{"todo.md", ".", "todo.md is outside the folders the policy allows. A person can allow it: holdfast allow files ."},
		{"~/a b/c", "~/a b", "~/a b/c is outside the folders the policy allows. A person can allow it: holdfast allow files '~/a b'"},
		{"x ~/f", "x ~", "x ~/f is outside the folders the policy allows. A person can allow it: holdfast allow files 'x ~'"},
		{"$(id)/f", "$(id)", "$(id)/f is outside the folders the policy allows. A person can allow it: holdfast allow files '$(id)'"},
	}
	for _, tt := range tests {
		if got := Outside(tt.path, tt.folder).Error(); got != tt.want {
			t.Errorf("Outside(%q, %q)\n got %q\nwant %q", tt.path, tt.folder, got, tt.want)
		}
	}
}

// A check an agent proposed decides part of the suggested command. Whatever it
// carries, the suggestion stays one command with the words as they were given.
func TestSuggestionsAreSafeToPaste(t *testing.T) {
	p := mustParse(t, "")
	for _, w := range hostile {
		if w == "" {
			continue
		}
		texts := map[string]string{
			verdict(t, p.Host(w)):      shellWord(normHost(w)),
			Outside(w+"/f", w).Error(): shellWord(w),
		}
		// Evidence is one line with single spaces, so a command is only
		// suggested exactly when it can be shown that way.
		if fact.CleanLine(w, 120) == w {
			if w != "*" { // as an argument a rule reads it as "anything", so no line is suggested
				texts[verdict(t, p.Command([]string{"prog", w}))] = shellWord(w)
			}
			texts[verdict(t, p.Command([]string{w}))] = shellWord(w)
		}
		for text, word := range texts {
			_, fix, ok := strings.Cut(text, ". A person can allow it: holdfast allow ")
			if !ok {
				t.Errorf("no suggestion in %q", text)
				continue
			}
			if !strings.HasSuffix(fix, " "+word) {
				t.Errorf("for %q the suggestion is %q, want it to end in %s", w, fix, word)
			}
		}
	}
	got := verdict(t, p.Host("$(id).example.com"))
	want := "the policy does not allow connections to $(id).example.com. A person can allow it: holdfast allow net '$(id).example.com'"
	if got != want {
		t.Errorf("Host:\n got %q\nwant %q", got, want)
	}
	got = verdict(t, mustParse(t, "net loopback").Conn("a;id", ip("10.0.0.1")))
	want = "the policy does not allow connections to a;id (10.0.0.1, a private address). A person can allow it: holdfast allow net 'a;id'"
	if got != want {
		t.Errorf("Conn:\n got %q\nwant %q", got, want)
	}
}

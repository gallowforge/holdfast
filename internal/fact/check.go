package fact

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Check is a declarative test of a claim. It names what to look at and what
// must be seen there; it is never a script. The same grammar is used on the
// command line, by agents and in the ledger:
//
//	file <path>                      the path exists
//	contains <path> <text>           the file contains the text
//	matches <path> <regex>           the file matches the regular expression
//	unchanged <path>                 the file is byte-for-byte what it was when the fact was stored
//	fresh <path> <duration>          the file was modified within the duration (26h, 7d)
//	http <status> <url> [<text>]     a GET answers with the status (and the body contains the text)
//	port <host:port>                 the port accepts a TCP connection
//	dns <name> [<ip>]                the name resolves (and one of its addresses is ip)
//	cmd <program> [args...]          the command exits 0; only commands a person has allowed
//	says <text> <program> [args...]  the command exits 0 and prints the text
//
// Any check may start with "not" to assert the opposite.
type Check struct {
	Kind   string   `json:"kind"`
	Not    bool     `json:"not,omitempty"`
	Path   string   `json:"path,omitempty"`
	Text   string   `json:"text,omitempty"`
	Regex  string   `json:"regex,omitempty"`
	Sum    string   `json:"sha256,omitempty"`
	URL    string   `json:"url,omitempty"`
	Code   int      `json:"code,omitempty"`
	Addr   string   `json:"addr,omitempty"`
	Name   string   `json:"name,omitempty"`
	IP     string   `json:"ip,omitempty"`
	Within Seconds  `json:"within_s,omitempty"`
	Argv   []string `json:"argv,omitempty"`
}

// Local reports whether the check only reads the project's files. A check
// like that is cheap enough to run every time it is asked about; the others
// wait on a network or a program.
func (c *Check) Local() bool {
	switch c.Kind {
	case "file", "contains", "matches", "unchanged", "fresh":
		return true
	}
	return false
}

// Kinds lists every kind of check, in the order the help shows them.
var Kinds = []string{"file", "contains", "matches", "unchanged", "fresh", "http", "port", "dns", "cmd", "says"}

// Grammar is the one-screen description of the check language, shown in help
// text and sent to agents with the tool definitions.
const Grammar = `file <path>                      the path exists
contains <path> <text>           the file contains the text
matches <path> <regex>           the file matches the regular expression (RE2)
unchanged <path>                 the file is exactly what it was when the fact was stored
fresh <path> <duration>          the file was modified within the duration (90s, 26h, 7d)
http <status> <url> [<text>]     a GET answers with the status (and the body contains the text)
port <host:port>                 the port accepts a TCP connection
dns <name> [<ip>]                the name resolves (and one of its addresses is the ip)
cmd <program> [args...]          the command exits 0 (only commands a person has allowed)
says <text> <program> [args...]  the command exits 0 and its output contains the text
Start any check with "not" to assert the opposite. Quote arguments that contain spaces.`

const maxRegex = 512

// maxProgram is how large a pattern may be once it is compiled. Matching
// takes time in proportion to the size of the file times the size of this
// program, and a short pattern can compile to a huge one:
// (?:[a-z ]{1,30}){1,30} is 22 bytes and 1,800 steps.
const maxProgram = 1000

// ParseCheck reads a check written in the grammar above.
func ParseCheck(s string) (*Check, error) {
	if len(s) > MaxCheck {
		return nil, fmt.Errorf("the check is %d bytes; the limit is %d", len(s), MaxCheck)
	}
	tok, err := Split(s)
	if err != nil {
		return nil, err
	}
	c := &Check{}
	if len(tok) > 0 && tok[0] == "not" {
		c.Not = true
		tok = tok[1:]
	}
	if len(tok) == 0 {
		return nil, errors.New("the check is empty. " + kindsHint())
	}
	c.Kind = tok[0]
	args := tok[1:]
	want := func(min, max int, usage string) error {
		if len(args) < min || len(args) > max {
			return fmt.Errorf("%s takes %s: %s", c.Kind, plural(min, max), usage)
		}
		return nil
	}
	switch c.Kind {
	case "file":
		if err := want(1, 1, "file <path>"); err != nil {
			return nil, err
		}
		c.Path = args[0]
	case "contains":
		if err := want(2, 2, `contains <path> <text>  (quote the text if it has spaces)`); err != nil {
			return nil, err
		}
		c.Path, c.Text = args[0], args[1]
	case "matches":
		if err := want(2, 2, `matches <path> <regex>`); err != nil {
			return nil, err
		}
		c.Path, c.Regex = args[0], args[1]
	case "unchanged":
		// The digest is recorded by Holdfast when the fact is stored; a stored
		// check is written back as "unchanged <path> <sha256>".
		if err := want(1, 2, "unchanged <path>"); err != nil {
			return nil, err
		}
		c.Path = args[0]
		if len(args) == 2 {
			c.Sum = strings.ToLower(args[1])
		}
	case "fresh":
		if err := want(2, 2, "fresh <path> <duration>, for example: fresh backups/latest.tar 26h"); err != nil {
			return nil, err
		}
		d, err := ParseDuration(args[1])
		if err != nil {
			return nil, err
		}
		c.Path, c.Within = args[0], Seconds(d/time.Second)
	case "http":
		if err := want(2, 3, "http <status> <url> [<text>], for example: http 200 https://example.com/health"); err != nil {
			return nil, err
		}
		code, err := strconv.Atoi(args[0])
		if err != nil {
			return nil, fmt.Errorf("http: %q is not a status code. Write: http 200 https://example.com/health", args[0])
		}
		c.Code, c.URL = code, args[1]
		if len(args) == 3 {
			c.Text = args[2]
		}
	case "port":
		if err := want(1, 1, "port <host:port>, for example: port localhost:5432"); err != nil {
			return nil, err
		}
		c.Addr = args[0]
	case "dns":
		if err := want(1, 2, "dns <name> [<ip>]"); err != nil {
			return nil, err
		}
		c.Name = strings.ToLower(strings.TrimSuffix(args[0], "."))
		if len(args) == 2 {
			c.IP = args[1]
		}
	case "cmd":
		if len(args) == 0 {
			return nil, errors.New("cmd takes a program and its arguments: cmd systemctl is-active docker")
		}
		c.Argv = args
	case "says":
		if len(args) < 2 {
			return nil, errors.New(`says takes the expected text, then a program and its arguments: says active systemctl is-active docker`)
		}
		c.Text, c.Argv = args[0], args[1:]
	default:
		return nil, fmt.Errorf("unknown check %q. %s", c.Kind, kindsHint())
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	// The check is stored as String writes it, and that line must be readable
	// again: quoting can make it longer than what was typed, and "unchanged"
	// gains a digest when the fact is stored.
	n := len(c.String())
	if c.Kind == "unchanged" && c.Sum == "" {
		n += 1 + sha256.Size*2
	}
	if n > MaxCheck {
		return nil, fmt.Errorf("the check is %d bytes once written out; the limit is %d", n, MaxCheck)
	}
	return c, nil
}

func kindsHint() string {
	return "A check starts with one of: " + strings.Join(Kinds, ", ") + `, optionally after "not"`
}

func plural(min, max int) string {
	if min == max {
		if min == 1 {
			return "1 argument"
		}
		return fmt.Sprintf("%d arguments", min)
	}
	return fmt.Sprintf("%d or %d arguments", min, max)
}

// Validate checks that a check is well formed. It does not decide whether the
// policy allows it: that happens every time the check runs.
func (c *Check) Validate() error {
	for _, s := range append([]string{c.Path, c.Text, c.Regex, c.URL, c.Addr, c.Name, c.IP}, c.Argv...) {
		for _, r := range s {
			// The same characters CleanLine removes from free text: the C0 and C1
			// controls (a terminal obeys both) and the Unicode line breaks.
			if unicode.IsControl(r) || r == 0x2028 || r == 0x2029 {
				return fmt.Errorf("%s: an argument contains a control character; a check is one plain line", c.Kind)
			}
		}
	}
	needPath := func() error {
		if c.Path == "" {
			return fmt.Errorf("%s: the path is empty", c.Kind)
		}
		if strings.ContainsRune(c.Path, 0) {
			return fmt.Errorf("%s: the path contains a NUL byte", c.Kind)
		}
		return nil
	}
	switch c.Kind {
	case "file":
		return needPath()
	case "contains":
		if c.Text == "" {
			return errors.New("contains: the text to look for is empty")
		}
		return needPath()
	case "matches":
		if len(c.Regex) > maxRegex {
			return fmt.Errorf("matches: the regular expression is longer than %d bytes", maxRegex)
		}
		if c.Regex == "" {
			return errors.New("matches: the regular expression is empty")
		}
		if _, err := Pattern(c.Regex); err != nil {
			return fmt.Errorf("matches: %v", err)
		}
		return needPath()
	case "unchanged":
		if c.Sum != "" && !isHex64(c.Sum) {
			return errors.New("unchanged: the recorded digest is not a sha256")
		}
		return needPath()
	case "fresh":
		if c.Within <= 0 {
			return errors.New("fresh: the duration must be more than zero")
		}
		return needPath()
	case "http":
		if c.Code < 100 || c.Code > 599 {
			return fmt.Errorf("http: %d is not a status code", c.Code)
		}
		u, err := url.Parse(c.URL)
		// Hostname, not Host: "http://:8080/" has a port and still names no host.
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("http: %q is not an http or https address", c.URL)
		}
		if u.User != nil {
			return errors.New("http: the address carries a user name or password. Holdfast stores checks in plain text; never put credentials in one")
		}
		return nil
	case "port":
		host, port, err := net.SplitHostPort(c.Addr)
		if err != nil || host == "" {
			return fmt.Errorf("port: %q is not host:port", c.Addr)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("port: %q is not a port number", port)
		}
		return nil
	case "dns":
		// ParseCheck drops one trailing dot; a name that still ends in one would
		// not read back as itself.
		if c.Name == "" || strings.ContainsAny(c.Name, " /:@") || strings.HasSuffix(c.Name, ".") {
			return fmt.Errorf("dns: %q is not a host name", c.Name)
		}
		if c.IP != "" && net.ParseIP(c.IP) == nil {
			return fmt.Errorf("dns: %q is not an IP address", c.IP)
		}
		return nil
	case "cmd", "says":
		if len(c.Argv) == 0 || c.Argv[0] == "" {
			return fmt.Errorf("%s: no program given", c.Kind)
		}
		if c.Kind == "says" && c.Text == "" {
			return errors.New("says: the expected text is empty")
		}
		for _, a := range c.Argv {
			if strings.ContainsRune(a, 0) {
				return fmt.Errorf("%s: an argument contains a NUL byte", c.Kind)
			}
		}
		return nil
	}
	return fmt.Errorf("unknown check %q. %s", c.Kind, kindsHint())
}

// String writes the check in the grammar ParseCheck reads; the two round-trip.
func (c *Check) String() string {
	var t []string
	if c.Not {
		t = append(t, "not")
	}
	t = append(t, c.Kind)
	switch c.Kind {
	case "file":
		t = append(t, c.Path)
	case "contains":
		t = append(t, c.Path, c.Text)
	case "matches":
		t = append(t, c.Path, c.Regex)
	case "unchanged":
		t = append(t, c.Path)
		if c.Sum != "" {
			t = append(t, c.Sum)
		}
	case "fresh":
		t = append(t, c.Path, FormatDuration(c.Within.Duration()))
	case "http":
		t = append(t, strconv.Itoa(c.Code), c.URL)
		if c.Text != "" {
			t = append(t, c.Text)
		}
	case "port":
		t = append(t, c.Addr)
	case "dns":
		t = append(t, c.Name)
		if c.IP != "" {
			t = append(t, c.IP)
		}
	case "cmd":
		t = append(t, c.Argv...)
	case "says":
		t = append(t, c.Text)
		t = append(t, c.Argv...)
	}
	for i := range t {
		t[i] = Quote(t[i])
	}
	return strings.Join(t, " ")
}

// Short is String without the recorded digest: what a person typed.
func (c *Check) Short() string {
	if c.Kind == "unchanged" && c.Sum != "" {
		d := *c
		d.Sum = ""
		return d.String()
	}
	return c.String()
}

// Equal reports whether two checks are the same test.
func (c *Check) Equal(o *Check) bool {
	if c == nil || o == nil {
		return c == o
	}
	return c.String() == o.String()
}

// Split cuts a line into words the way a shell would, without running one:
// spaces separate words, "double quotes" keep spaces and understand \" and \\,
// 'single quotes' keep everything as written.
func Split(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in := false // inside a word (so "" is a word)
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		switch ch := r[i]; {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		case ch == '\'':
			in = true
			j := i + 1
			for j < len(r) && r[j] != '\'' {
				cur.WriteRune(r[j])
				j++
			}
			if j == len(r) {
				return nil, errors.New("a single quote is never closed")
			}
			i = j
		case ch == '"':
			in = true
			j := i + 1
			for ; j < len(r) && r[j] != '"'; j++ {
				if r[j] == '\\' && j+1 < len(r) && (r[j+1] == '"' || r[j+1] == '\\') {
					j++
				}
				cur.WriteRune(r[j])
			}
			if j == len(r) {
				return nil, errors.New("a double quote is never closed")
			}
			i = j
		default:
			in = true
			cur.WriteRune(ch)
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}

// Pattern compiles the regular expression of a "matches" check the way grep
// reads one: ^ and $ are the start and the end of any line of the file, not
// only of the file as a whole (\A and \z still mean that). The check keeps the
// expression as it was written; the line mode is added here and nowhere else.
//
// A pattern that would be slow to run is refused here, so that it is turned
// away when a fact is stored and is never run from a journal either.
func Pattern(expr string) (*regexp.Regexp, error) {
	if steps("(?m)"+expr) > maxProgram {
		return nil, errors.New("the pattern is too complicated to run quickly; make it simpler")
	}
	re, err := regexp.Compile("(?m)" + expr)
	if err != nil {
		// Report the mistake against what was written, not what was compiled.
		if _, plain := regexp.Compile(expr); plain != nil {
			err = plain
		}
		return nil, err
	}
	return re, nil
}

// steps is the size of the program a pattern compiles to, worked out the way
// the regexp package does it, or 0 for a pattern that does not compile.
func steps(expr string) int {
	tree, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return 0
	}
	prog, err := syntax.Compile(tree.Simplify())
	if err != nil {
		return 0
	}
	return len(prog.Inst)
}

// Quote writes one word so that Split reads it back unchanged.
func Quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\r\"'\\") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

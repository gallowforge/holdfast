// Package policy decides what a check may touch. Agents propose checks; the
// policy is a file only a person edits, and nothing an agent can reach through
// Holdfast widens it.
package policy

import (
	"bufio"
	"fmt"
	"net/netip"
	"strings"
	"unicode"

	"github.com/gallowforge/holdfast/internal/fact"
)

// Policy is the parsed policy file.
type Policy struct {
	// Files lists the folders checks may read, as written (relative to the
	// project root, absolute, or starting with ~/).
	Files []string
	// Net lists who checks may connect to.
	Net []NetRule
	// Cmds lists the commands checks may run, one argument pattern per entry.
	Cmds [][]string
}

// NetRule allows connections by address class, by address range or by name.
type NetRule struct {
	Class  string       // "loopback", "private" or "public"
	Prefix netip.Prefix // an address or a range
	Name   string       // "example.com" or "*.example.com"
}

func (r NetRule) String() string {
	switch {
	case r.Class != "":
		return r.Class
	case r.Name != "":
		return r.Name
	case r.Prefix.IsSingleIP():
		return r.Prefix.Addr().String()
	default:
		return r.Prefix.String()
	}
}

// Default is the policy a new store starts with: checks may read the project
// and talk to this machine, and nothing else.
func Default() *Policy {
	return &Policy{Files: []string{"."}, Net: []NetRule{{Class: "loopback"}}}
}

// Header is written at the top of a new policy file.
const Header = `# Holdfast policy: what checks may touch.
#
# Agents can propose checks. Only a person widens this file, by editing it or
# with "holdfast allow". A check outside the policy is not run; its fact shows
# as blocked until a person decides.
#
#   files <folder>...     folders checks may read (relative to the project root)
#   net   <who>...        loopback, private, public, a host name (example.com,
#                         *.example.com), an address or a range (10.0.0.0/8)
#   cmd   <program> ...   one command checks may run; * stands for any one
#                         argument, ** at the end for any further arguments
`

// Parse reads a policy file.
func Parse(text string) (*Policy, error) {
	p := &Policy{}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		// The file is shown to the person who approves it. A character that
		// moves the cursor or clears the screen could hide a rule from them,
		// so no line may hold one, not even a comment.
		if strings.ContainsFunc(sc.Text(), hidden) {
			return nil, fmt.Errorf("policy line %d: there is a control character in this line, which could hide what the line says when it is shown; remove it", n)
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		words, err := fact.Split(line)
		if err != nil {
			return nil, fmt.Errorf("policy line %d: %v", n, err)
		}
		args := words[1:]
		if len(args) == 0 {
			return nil, fmt.Errorf("policy line %d: %q allows nothing; remove the line or finish it", n, words[0])
		}
		switch words[0] {
		case "files":
			p.Files = append(p.Files, args...)
		case "net":
			for _, a := range args {
				r, err := ParseNetRule(a)
				if err != nil {
					return nil, fmt.Errorf("policy line %d: %v", n, err)
				}
				p.Net = append(p.Net, r)
			}
		case "cmd":
			if err := checkPattern(args); err != nil {
				return nil, fmt.Errorf("policy line %d: %v", n, err)
			}
			p.Cmds = append(p.Cmds, args)
		default:
			return nil, fmt.Errorf("policy line %d: unknown word %q (expected files, net or cmd)", n, words[0])
		}
	}
	return p, sc.Err()
}

// hidden reports whether r could change what a terminal shows of the line it
// is in: move the cursor, clear the screen, start a new line, or draw the
// line in another order. A tab is the one control character a policy file
// may hold.
func hidden(r rune) bool {
	return (unicode.IsControl(r) && r != '\t') || r == 0x2028 || r == 0x2029 || fact.Reorders(r)
}

// Printable returns the text of a policy file in a form that is safe to show
// on a terminal whether or not the file parses: line by line, with a mark that
// can be seen in place of every character Parse refuses. A file that parses
// comes back as it was written.
func Printable(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		// A line written on Windows ends in a carriage return, as Parse allows.
		line = strings.TrimSuffix(line, "\r")
		lines[i] = strings.Map(func(r rune) rune {
			if hidden(r) {
				return unicode.ReplacementChar
			}
			return r
		}, line)
	}
	return strings.Join(lines, "\n")
}

// ParseNetRule reads one word of a "net" line.
func ParseNetRule(s string) (NetRule, error) {
	low := strings.ToLower(strings.TrimSuffix(s, "."))
	switch low {
	case "loopback", "private", "public":
		return NetRule{Class: low}, nil
	case "localhost":
		return NetRule{Class: "loopback"}, nil
	case "":
		return NetRule{}, fmt.Errorf("an empty net rule")
	}
	if pfx, err := netip.ParsePrefix(low); err == nil {
		return NetRule{Prefix: pfx.Masked()}, nil
	}
	if a, err := netip.ParseAddr(low); err == nil {
		a = a.Unmap()
		return NetRule{Prefix: netip.PrefixFrom(a, a.BitLen())}, nil
	}
	name := strings.TrimPrefix(low, "*.")
	// No white space or quoting characters either: no host has them, and a tab
	// inside a name would read back from the file as two rules.
	odd := func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("*/:@\"'\\", r)
	}
	if name == "" || strings.ContainsFunc(name, odd) || strings.HasPrefix(name, ".") || low == "*" {
		return NetRule{}, fmt.Errorf("net rule %q: write loopback, private, public, a host name, *.a-domain, an address or a range", s)
	}
	return NetRule{Name: low}, nil
}

func checkPattern(args []string) error {
	if args[0] == "*" || args[0] == "**" {
		return fmt.Errorf("a cmd rule must name its program; %q would allow every command", args[0])
	}
	for i, a := range args {
		if a == "**" && i != len(args)-1 {
			return fmt.Errorf("** may only end a cmd rule")
		}
	}
	return nil
}

// String writes the policy as a file Parse reads back.
func (p *Policy) String() string {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteByte('\n')
	if len(p.Files) > 0 {
		b.WriteString("files")
		for _, f := range p.Files {
			b.WriteString(" " + fact.Quote(f))
		}
		b.WriteByte('\n')
	}
	if len(p.Net) > 0 {
		b.WriteString("net")
		for _, r := range p.Net {
			b.WriteString(" " + fact.Quote(r.String()))
		}
		b.WriteByte('\n')
	}
	for _, c := range p.Cmds {
		b.WriteString("cmd")
		for _, a := range c {
			b.WriteString(" " + fact.Quote(a))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Class sorts an address into loopback, private, public, link-local or never.
// Link-local holds cloud metadata services and is never allowed by class; the
// "never" addresses (multicast, broadcast) are not allowed at all.
func Class(ip netip.Addr) string {
	ip = ip.Unmap()
	switch {
	case !ip.IsValid():
		return "never"
	case ip.IsLoopback() || ip.IsUnspecified():
		return "loopback"
	case ip.IsLinkLocalUnicast():
		return "link-local"
	case ip.IsMulticast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast():
		return "never"
	case ip.Is4() && ip.As4() == [4]byte{255, 255, 255, 255}:
		return "never"
	case ip.IsPrivate() || cgnat.Contains(ip):
		return "private"
	default:
		return "public"
	}
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10") // carrier NAT and overlay networks

// Blocked is the error for anything the policy does not allow. Its text is
// shown to people and agents as the evidence of a blocked fact.
type Blocked struct{ Why, Fix string }

func (b *Blocked) Error() string {
	if b.Fix == "" {
		return b.Why
	}
	return b.Why + ". A person can allow it: " + b.Fix
}

// Outside is the refusal for a path that none of the allowed folders holds.
// folder is the one a person would add: the folder that holds the path,
// written the way a policy file writes folders.
func Outside(path, folder string) *Blocked {
	return &Blocked{Why: path + " is outside the folders the policy allows", Fix: "holdfast allow files " + shellWord(folder)}
}

// shellWord writes one word of a suggested command so that a shell reads it
// back as that word and nothing more. The suggestion is there to be pasted
// into a terminal, and the word may come from a check an agent proposed: it
// must not be able to start a second command, expand a variable or a wildcard,
// or split into two folders.
func shellWord(s string) string {
	rest := s
	if s == "~" || strings.HasPrefix(s, "~/") {
		rest = s[1:] // the shell and Holdfast read a leading ~ the same way
	}
	plain := s != ""
	for i, r := range rest {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), strings.ContainsRune("_-./:@+,", r):
		case r == '=' && i > 0:
		default:
			plain = false
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Host is the early answer for a host name, before anything is resolved: nil
// if a connection might be allowed, a *Blocked if no rule could allow it.
//
// It also decides whether the name may be looked up at all. A lookup sends the
// name to a DNS server, so a name is only resolved when a person has named it,
// or has opened the private or public network as a whole.
func (p *Policy) Host(host string) error {
	host = normHost(host)
	if ip, err := netip.ParseAddr(host); err == nil {
		return p.Conn(host, ip)
	}
	local := localName(host)
	for _, r := range p.Net {
		switch {
		case matchName(r.Name, host):
			return nil
		case local && (r.Class == "loopback" || (r.Prefix.IsValid() && r.Prefix.Addr().IsLoopback())):
			return nil
		case !local && (r.Class == "private" || r.Class == "public"):
			return nil
		}
	}
	return &Blocked{Why: "the policy does not allow connections to " + host, Fix: "holdfast allow net " + shellWord(host)}
}

// Conn is the final answer, asked with the address actually being dialled, so
// a name cannot be pointed somewhere the policy would not allow by address.
func (p *Policy) Conn(host string, ip netip.Addr) error {
	host = normHost(host)
	ip = ip.Unmap()
	class := Class(ip)
	if class == "never" {
		return &Blocked{Why: "Holdfast never connects to " + ip.String()}
	}
	_, literal := netip.ParseAddr(host)
	for _, r := range p.Net {
		switch {
		case r.Prefix.IsValid():
			if r.Prefix.Contains(ip) {
				return nil
			}
		case r.Class != "":
			if r.Class == class {
				return nil
			}
		case literal != nil && matchName(r.Name, host):
			// A host a person named is trusted wherever it lives, except in the
			// link-local range (cloud metadata), which needs its address spelled out.
			if class != "link-local" {
				return nil
			}
		}
	}
	if class == "link-local" {
		return &Blocked{Why: fmt.Sprintf("%s is a link-local address (%s), which is only allowed by its exact address", host, ip), Fix: "holdfast allow net " + ip.String()}
	}
	what := host
	if literal != nil && host != ip.String() {
		what = fmt.Sprintf("%s (%s, a %s address)", host, ip, class)
	}
	fix := host
	if literal == nil {
		fix = ip.String()
	}
	return &Blocked{Why: "the policy does not allow connections to " + what, Fix: "holdfast allow net " + shellWord(fix)}
}

// LocalName reports whether host is a name for this machine itself: localhost,
// or any name that ends in .localhost. Such a name is never sent to a DNS
// server, where it would tell the network what is being checked and could be
// answered with any address; it stands for the addresses Loopback returns.
func LocalName(host string) bool { return localName(normHost(host)) }

func localName(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

// Loopback is what a local name stands for: this machine, by both of its
// loopback addresses.
func Loopback() []netip.Addr {
	return []netip.Addr{netip.AddrFrom4([4]byte{127, 0, 0, 1}), netip.IPv6Loopback()}
}

func normHost(h string) string {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	return h
}

func matchName(rule, host string) bool {
	if rule == "" {
		return false
	}
	if strings.HasPrefix(rule, "*.") {
		return strings.HasSuffix(host, rule[1:]) && len(host) > len(rule)-1
	}
	return rule == host
}

// Command reports whether the policy allows running argv.
func (p *Policy) Command(argv []string) error {
	for _, pat := range p.Cmds {
		if matchArgv(pat, argv) {
			return nil
		}
	}
	q, sh := make([]string, len(argv)), make([]string, len(argv))
	wild := ""
	for i, a := range argv {
		q[i], sh[i] = fact.Quote(a), shellWord(a)
		if i > 0 && wild == "" && (a == "*" || a == "**") {
			wild = a
		}
	}
	line := strings.Join(sh, " ")
	b := &Blocked{
		Why: "the policy does not allow the command: " + fact.CleanLine(strings.Join(q, " "), 120),
		Fix: "holdfast allow cmd " + fact.CleanLine(line, 120),
	}
	// The line is there to be pasted, so it is only given when pasting it
	// allows this command and no other.
	switch {
	case wild != "":
		// "allow cmd echo '*'" writes the rule "cmd echo *", and that is echo
		// with any argument at all, not echo with a star.
		b.Fix = "by hand only. The policy reads its argument " + wild + ` as "anything", so the rule must be written into the policy file`
	case fact.CleanLine(line, 120) != fact.CleanLine(line, len(line)) || len([]rune(b.Error())) > fact.MaxDetail:
		// A line that lost its end is a rule for some other command, and
		// evidence is cut at MaxDetail whatever it says.
		b.Fix = "by hand only. The command is too long to suggest a line for, so the rule must be written into the policy file"
	}
	return b
}

func matchArgv(pat, argv []string) bool {
	for i, p := range pat {
		if p == "**" {
			return true
		}
		if i >= len(argv) {
			return false
		}
		// The program itself is never a wildcard, and is matched exactly: a rule
		// for "git" does not allow "./git" or "/tmp/git".
		if p != argv[i] && (p != "*" || i == 0) {
			return false
		}
	}
	return len(argv) == len(pat)
}

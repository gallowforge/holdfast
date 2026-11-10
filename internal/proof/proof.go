// Package proof runs a fact's check against a world and reports what it saw.
package proof

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/world"
)

// Limits on how much a single check may read or wait for.
const (
	Timeout        = 5 * time.Second  // files, http, port, dns
	CommandTimeout = 10 * time.Second // cmd, says
	MaxScan        = 8 << 20          // contains, matches: bytes of file read
	MaxHash        = 1 << 30          // unchanged: bytes of file hashed
	MaxBody        = 1 << 20          // http: bytes of body read
	MaxOutput      = 64 << 10         // cmd, says: bytes of output kept
)

// Run proves or disproves one check. It always returns evidence: a failure to
// run is evidence too, with the status "blocked".
func Run(ctx context.Context, w world.World, p *policy.Policy, c *fact.Check) fact.Evidence {
	limit := Timeout
	if c.Kind == "cmd" || c.Kind == "says" {
		limit = CommandTimeout
	}
	caller := ctx
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	start := time.Now()
	ok, detail, err := observe(ctx, w, p, c, limit)
	took := time.Since(start)
	// A caller that stopped waiting (a shutdown, a spent budget) cut the check
	// short. Anything short of a plain yes seen while being stopped is not
	// evidence: it must not break a fact, nor make a "not" fact hold.
	if !ok && caller.Err() != nil && !isBlocked(err) {
		err = errors.New("the check was stopped before it finished")
	}
	if pacer, simulated := w.(world.Pacer); simulated {
		took = pacer.Took(c.Kind, target(c))
	}
	ev := fact.Evidence{At: w.Now().UTC().Truncate(time.Millisecond), Ms: took.Milliseconds()}

	var blocked *policy.Blocked
	switch {
	case errors.As(err, &blocked):
		// A long path must not push the command off the end of the line.
		blocked = blocked.Within(fact.MaxDetail)
		ev.Status, ev.Detail = fact.Blocked, blocked.Error()
		ev.Fix = fact.WholeLine(blocked.Fix, fact.MaxDetail)
	case err != nil:
		ev.Status, ev.Detail = fact.Blocked, "could not check: "+err.Error()
	case ok != c.Not:
		ev.Status, ev.Detail = fact.Holds, detail
	default:
		ev.Status, ev.Detail = fact.Broken, detail
	}
	ev.Detail = fact.CleanLine(ev.Detail, fact.MaxDetail)
	return ev
}

// Seal fills in what a check must remember from the moment a fact is stored:
// for "unchanged", the digest of the file as it is now.
func Seal(ctx context.Context, w world.World, p *policy.Policy, c *fact.Check) error {
	if c.Kind != "unchanged" || c.Sum != "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	sum, err := digest(ctx, w, p, c.Path)
	if err != nil {
		return err
	}
	if sum == "" {
		return fmt.Errorf("%s does not exist, so there is nothing to keep unchanged", c.Path)
	}
	c.Sum = sum
	return nil
}

func target(c *fact.Check) string {
	switch c.Kind {
	case "http":
		return c.URL
	case "port":
		return c.Addr
	case "dns":
		return c.Name
	case "cmd", "says":
		return strings.Join(c.Argv, " ")
	}
	return c.Path
}

// observe looks at the world. ok says whether the positive form of the check
// is true; detail says what was seen, in words that read correctly whether or
// not the check is negated. An error means the check could not be run.
func observe(ctx context.Context, w world.World, p *policy.Policy, c *fact.Check, limit time.Duration) (ok bool, detail string, err error) {
	switch c.Kind {
	case "file":
		st, err := w.Stat(p.Files, c.Path)
		if err != nil {
			return false, "", fileErr(c.Path, err)
		}
		if !st.Exists {
			return false, c.Path + " does not exist", nil
		}
		return true, c.Path + " exists", nil

	case "contains", "matches":
		data, missing, err := read(w, p, c.Path)
		if err != nil {
			return false, "", err
		}
		if missing {
			return false, c.Path + " does not exist", nil
		}
		if c.Kind == "contains" {
			i := bytes.Index(data, []byte(c.Text))
			if i < 0 {
				return false, fmt.Sprintf("%s does not contain %s", c.Path, quote(c.Text)), nil
			}
			return true, fmt.Sprintf("%s contains %s (line %d)", c.Path, quote(c.Text), 1+bytes.Count(data[:i], []byte("\n"))), nil
		}
		re, err := fact.Pattern(c.Regex) // ^ and $ are the ends of a line, as in grep
		if err != nil {
			return false, "", err
		}
		// The regexp package cannot be told to stop, and a pattern can be
		// slow on a large file, so the file is handed to it through a reader
		// that runs dry when the check's time is up.
		loc := re.FindReaderIndex(&patient{ctx: ctx, r: bytes.NewReader(data)})
		if ctx.Err() != nil {
			return false, "", fmt.Errorf("searching %s took too long", c.Path)
		}
		if loc == nil {
			return false, fmt.Sprintf("%s does not match %s", c.Path, quote(c.Regex)), nil
		}
		return true, fmt.Sprintf("%s matches %s (line %d)", c.Path, quote(c.Regex), 1+bytes.Count(data[:loc[0]], []byte("\n"))), nil

	case "unchanged":
		if c.Sum == "" {
			return false, "", errors.New("no digest was recorded for " + c.Path)
		}
		sum, err := digest(ctx, w, p, c.Path)
		if err != nil {
			return false, "", err
		}
		switch sum {
		case "":
			return false, c.Path + " does not exist any more", nil
		case c.Sum:
			return true, fmt.Sprintf("%s is unchanged (sha256 %s)", c.Path, short(sum)), nil
		default:
			return false, fmt.Sprintf("%s has changed (sha256 was %s, is now %s)", c.Path, short(c.Sum), short(sum)), nil
		}

	case "fresh":
		st, err := w.Stat(p.Files, c.Path)
		if err != nil {
			return false, "", fileErr(c.Path, err)
		}
		if !st.Exists {
			return false, c.Path + " does not exist", nil
		}
		age := w.Now().Sub(st.ModTime)
		within := c.Within.Duration()
		if age > within {
			return false, fmt.Sprintf("%s was last modified %s ago, more than %s", c.Path, span(age), fact.FormatDuration(within)), nil
		}
		return true, fmt.Sprintf("%s was modified %s ago, within %s", c.Path, span(age), fact.FormatDuration(within)), nil

	case "http":
		u, err := url.Parse(c.URL)
		if err != nil {
			return false, "", err
		}
		if err := p.Host(u.Hostname()); err != nil {
			return false, "", err
		}
		want := int64(0)
		if c.Text != "" {
			want = MaxBody
		}
		status, body, err := w.Get(ctx, c.URL, p.Conn, want)
		if err != nil {
			if isBlocked(err) {
				return false, "", unwrapBlocked(err)
			}
			return false, fmt.Sprintf("%s did not answer: %s", shown(u), netErr(ctx, err, limit)), nil
		}
		if status != c.Code {
			return false, fmt.Sprintf("%s answered %d, not %d", shown(u), status, c.Code), nil
		}
		if c.Text != "" && !bytes.Contains(body, []byte(c.Text)) {
			return false, fmt.Sprintf("%s answered %d, but the body does not contain %s", shown(u), status, quote(c.Text)), nil
		}
		if c.Text != "" {
			return true, fmt.Sprintf("%s answered %d and the body contains %s", shown(u), status, quote(c.Text)), nil
		}
		return true, fmt.Sprintf("%s answered %d", shown(u), status), nil

	case "port":
		host, _, _ := net.SplitHostPort(c.Addr)
		if err := p.Host(host); err != nil {
			return false, "", err
		}
		if err := w.Dial(ctx, c.Addr, p.Conn); err != nil {
			if isBlocked(err) {
				return false, "", unwrapBlocked(err)
			}
			return false, fmt.Sprintf("%s does not accept connections: %s", c.Addr, netErr(ctx, err, limit)), nil
		}
		return true, c.Addr + " accepts connections", nil

	case "dns":
		if err := p.Host(c.Name); err != nil {
			return false, "", err
		}
		ips, err := w.Resolve(ctx, c.Name)
		if err != nil || len(ips) == 0 {
			why := "no addresses"
			if err != nil {
				why = netErr(ctx, err, limit)
			}
			return false, fmt.Sprintf("%s does not resolve: %s", c.Name, why), nil
		}
		list := make([]string, 0, len(ips))
		found := c.IP == ""
		want, _ := netip.ParseAddr(c.IP)
		for _, ip := range ips {
			list = append(list, ip.String())
			if c.IP != "" && ip.Unmap() == want.Unmap() {
				found = true
			}
		}
		if len(list) > 4 {
			list = append(list[:4], fmt.Sprintf("and %d more", len(list)-4))
		}
		if !found {
			return false, fmt.Sprintf("%s resolves to %s, not %s", c.Name, strings.Join(list, ", "), c.IP), nil
		}
		return true, fmt.Sprintf("%s resolves to %s", c.Name, strings.Join(list, ", ")), nil

	case "cmd", "says":
		if err := p.Command(c.Argv); err != nil {
			return false, "", err
		}
		line := strings.Join(c.Argv, " ")
		exit, out, err := w.Run(ctx, c.Argv, MaxOutput)
		if err != nil {
			if ctx.Err() != nil {
				return false, fmt.Sprintf("%s did not finish within %s", line, fact.FormatDuration(limit)), nil
			}
			return false, "", fmt.Errorf("%s could not be run: %v", line, err)
		}
		first := firstLine(out)
		if exit != 0 {
			if first != "" {
				return false, fmt.Sprintf("%s exited %d: %s", line, exit, first), nil
			}
			return false, fmt.Sprintf("%s exited %d", line, exit), nil
		}
		if c.Kind == "says" {
			if !bytes.Contains(out, []byte(c.Text)) {
				if first == "" {
					first = "nothing"
				}
				return false, fmt.Sprintf("%s exited 0 but did not print %s (it printed: %s)", line, quote(c.Text), first), nil
			}
			return true, fmt.Sprintf("%s printed %s", line, quote(c.Text)), nil
		}
		return true, line + " exited 0", nil
	}
	return false, "", fmt.Errorf("unknown check %q", c.Kind)
}

// read returns a file's bytes for scanning. missing is true when the path does
// not exist; anything unreadable or too large is an error (the check is blocked).
func read(w world.World, p *policy.Policy, path string) (data []byte, missing bool, err error) {
	st, err := w.Stat(p.Files, path)
	if err != nil {
		return nil, false, fileErr(path, err)
	}
	if !st.Exists {
		return nil, true, nil
	}
	if st.Dir {
		return nil, false, &policy.Blocked{Why: path + " is a folder, not a file"}
	}
	if st.Size > MaxScan {
		return nil, false, &policy.Blocked{Why: fmt.Sprintf("%s is larger than %d MiB; Holdfast does not scan files that large", path, MaxScan>>20)}
	}
	f, err := w.Open(p.Files, path)
	if err != nil {
		return nil, false, fileErr(path, err)
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, MaxScan+1))
	if err != nil {
		return nil, false, fileErr(path, err)
	}
	if len(data) > MaxScan {
		return nil, false, &policy.Blocked{Why: fmt.Sprintf("%s is larger than %d MiB; Holdfast does not scan files that large", path, MaxScan>>20)}
	}
	return data, false, nil
}

// patient reads a file to a pattern one character at a time, and stops at
// once when the context ends. What the pattern makes of a file cut short
// means nothing, so whoever uses it must look at the context afterwards.
type patient struct {
	ctx context.Context
	r   *bytes.Reader
	n   int
}

func (p *patient) ReadRune() (rune, int, error) {
	if p.n++; p.n%1024 == 0 && p.ctx.Err() != nil {
		return 0, 0, p.ctx.Err()
	}
	return p.r.ReadRune()
}

// digest returns the sha256 of a file, or "" if the path does not exist.
func digest(ctx context.Context, w world.World, p *policy.Policy, path string) (string, error) {
	st, err := w.Stat(p.Files, path)
	if err != nil {
		return "", fileErr(path, err)
	}
	if !st.Exists {
		return "", nil
	}
	if st.Dir {
		return "", &policy.Blocked{Why: path + " is a folder, not a file"}
	}
	if st.Size > MaxHash {
		return "", &policy.Blocked{Why: fmt.Sprintf("%s is larger than %d GiB", path, MaxHash>>30)}
	}
	f, err := w.Open(p.Files, path)
	if err != nil {
		return "", fileErr(path, err)
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 256<<10)
	for {
		if ctx.Err() != nil {
			return "", fmt.Errorf("reading %s took too long", path)
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fileErr(path, err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileErr(path string, err error) error {
	if isBlocked(err) {
		return unwrapBlocked(err)
	}
	// Keep the reason, drop the absolute path the operating system adds.
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	return &policy.Blocked{Why: "cannot read " + path + ": " + msg}
}

func isBlocked(err error) bool {
	var b *policy.Blocked
	return errors.As(err, &b)
}

func unwrapBlocked(err error) error {
	var b *policy.Blocked
	errors.As(err, &b)
	return b
}

// netErr says why a connection failed in a few plain words.
func netErr(ctx context.Context, err error, limit time.Duration) string {
	var dns *net.DNSError
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded:
		return "no answer within " + fact.FormatDuration(limit)
	case errors.As(err, &dns):
		if dns.IsNotFound {
			return "the name does not exist"
		}
		return "the name could not be looked up"
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "no route to host"):
		return "no route to host"
	case strings.Contains(msg, "network is unreachable"):
		return "network is unreachable"
	case strings.Contains(msg, "certificate"):
		return "the TLS certificate was not accepted"
	case strings.Contains(msg, "connection reset"):
		return "connection reset"
	}
	// The standard library quotes what the other side sent (a first line that
	// is not HTTP, a broken header, the address of a redirect). Evidence says
	// what went wrong, never what was sent.
	if i := strings.IndexByte(msg, '"'); i >= 0 {
		msg = strings.TrimRight(msg[:i], ": ")
	}
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	if msg == "" {
		msg = "the connection failed"
	}
	return msg
}

// shown is a URL as evidence shows it: without its query string, which is the
// part most likely to hold something private.
func shown(u *url.URL) string {
	v := *u
	v.RawQuery, v.Fragment, v.User = "", "", nil
	s := v.String()
	if u.RawQuery != "" {
		s += "?…"
	}
	return s
}

// quote puts text in evidence between quotation marks a reader cannot
// mistake: double ones, single ones when the text has a double one in it,
// and an escape only when it has both.
func quote(s string) string {
	s = fact.CleanLine(s, 60)
	switch {
	case !strings.Contains(s, `"`):
		return `"` + s + `"`
	case !strings.Contains(s, `'`):
		return `'` + s + `'`
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func short(sum string) string {
	if len(sum) < 12 {
		return sum
	}
	return sum[:8] + "…" + sum[len(sum)-4:]
}

func firstLine(out []byte) string {
	for _, l := range strings.Split(string(out), "\n") {
		if l = fact.CleanLine(l, 120); l != "" {
			return l
		}
	}
	return ""
}

// span writes an age in its two largest sensible units: 42s, 3h 10m, 3d 4h.
func span(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		days, h := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour)
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, h)
	}
}

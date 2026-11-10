// Package fact holds the things Holdfast remembers: a claim in plain words, the
// check that proves it, and the evidence from the last time the check ran.
package fact

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Status is what Holdfast can say about a fact right now.
type Status string

const (
	// Holds: the fact has a check and the check passed.
	Holds Status = "holds"
	// Broken: the fact has a check and the check failed.
	Broken Status = "broken"
	// Blocked: the fact has a check that Holdfast would not or could not run
	// (the policy does not allow it). It is neither proved nor disproved.
	Blocked Status = "blocked"
	// Unproven: the fact has no check. It is a note, with an author and an age.
	Unproven Status = "unproven"
	// Stale: an unproven fact that has outlived its time to live.
	Stale Status = "stale"
)

// Rank orders statuses by how much they need a person's attention.
func (s Status) Rank() int {
	switch s {
	case Broken:
		return 0
	case Blocked:
		return 1
	case Stale:
		return 2
	case Unproven:
		return 3
	default:
		return 4
	}
}

// DefaultTTL is how long a fact without a check is believed before it goes stale.
const DefaultTTL = 30 * 24 * time.Hour

// Limits on what a fact may hold. They keep one careless agent from filling the
// journal, and keep every fact short enough to read in a list.
const (
	MaxClaim    = 400
	MaxTags     = 8
	MaxTag      = 32
	MaxCheck    = 1000
	MaxDetail   = 300
	MaxAuthor   = 64
	MaxReason   = 300
	MaxHistory  = 40
	idAlphabet  = "23456789abcdefghjkmnpqrstvwxyz" // no 0/o, 1/l/i, u: easy to read back
	idLength    = 6
	idPrefix    = "f-"
	maxDuration = 10 * 365 * 24 * time.Hour
)

// Evidence is the outcome of running a fact's check once.
type Evidence struct {
	Status Status `json:"status"` // holds, broken or blocked
	Detail string `json:"detail"` // one plain line: what was seen
	// Fix is what a person can run to allow a blocked check. It is kept apart
	// from Detail because Detail quotes the check, and an agent wrote the
	// check: nobody should have to find the command inside text an agent shaped.
	Fix string    `json:"fix,omitempty"`
	At  time.Time `json:"at"`
	Ms  int64     `json:"ms"` // how long the check took
}

// Retraction records that a fact was withdrawn, by whom and why.
type Retraction struct {
	At     time.Time `json:"at"`
	By     string    `json:"by,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// Fact is one remembered claim.
type Fact struct {
	ID       string    `json:"id"`
	Claim    string    `json:"claim"`
	Tags     []string  `json:"tags,omitempty"`
	Check    *Check    `json:"check,omitempty"`
	By       string    `json:"by,omitempty"`
	Created  time.Time `json:"created"`
	TTL      Seconds   `json:"ttl_s,omitempty"`
	Replaces string    `json:"replaces,omitempty"`

	// Everything below is derived from the journal, never set by a caller.

	// Last is the most recent evidence; nil for a fact with no check.
	Last *Evidence `json:"last,omitempty"`
	// Since is when the fact entered its current proved state (holds, broken or
	// blocked): the moment of the last change, not of the last check.
	Since time.Time `json:"since,omitzero"`
	// Held is the last time the check passed.
	Held time.Time `json:"held,omitzero"`
	// Vouched is when a person last confirmed an unproven fact by hand.
	Vouched    time.Time   `json:"vouched,omitzero"`
	VouchedBy  string      `json:"vouched_by,omitempty"`
	Retracted  *Retraction `json:"retracted,omitempty"`
	ReplacedBy string      `json:"replaced_by,omitempty"`
	// History lists the changes of state, oldest first (capped).
	History []Evidence `json:"history,omitempty"`
}

// Seconds is a duration stored as whole seconds, so the journal stays readable.
type Seconds int64

func (s Seconds) Duration() time.Duration { return time.Duration(s) * time.Second }

// Live reports whether the fact still counts: not retracted and not replaced.
func (f *Fact) Live() bool { return f.Retracted == nil && f.ReplacedBy == "" }

// TimeToLive is the fact's own time to live, or the default.
func (f *Fact) TimeToLive() time.Duration {
	if f.TTL > 0 {
		return f.TTL.Duration()
	}
	return DefaultTTL
}

// NotedAt is when an unproven fact was last stated or vouched for by someone.
func (f *Fact) NotedAt() time.Time {
	if f.Vouched.After(f.Created) {
		return f.Vouched
	}
	return f.Created
}

// StatusAt is the fact's status at the given moment.
func (f *Fact) StatusAt(now time.Time) Status {
	if f.Check == nil {
		if now.Sub(f.NotedAt()) > f.TimeToLive() {
			return Stale
		}
		return Unproven
	}
	if f.Last == nil {
		return Blocked // has a check that has never run
	}
	return f.Last.Status
}

// CleanClaim trims a claim to one line of plain text and checks its length.
func CleanClaim(s string) (string, error) {
	s = oneLine(s)
	if s == "" {
		return "", errors.New("the claim is empty: say the fact in one plain sentence")
	}
	if n := len([]rune(s)); n > MaxClaim {
		return "", fmt.Errorf("the claim is %d characters; keep it under %d and put detail in the check", n, MaxClaim)
	}
	return s, nil
}

// CleanTags lowercases, de-duplicates and validates tags.
func CleanTags(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, t := range in {
		for _, part := range strings.FieldsFunc(t, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
			part = strings.ToLower(strings.TrimPrefix(part, "#"))
			if part == "" || seen[part] {
				continue
			}
			if len(part) > MaxTag {
				return nil, fmt.Errorf("tag %q is longer than %d characters", part, MaxTag)
			}
			for _, r := range part {
				if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '/') {
					return nil, fmt.Errorf("tag %q: use letters, digits, '-', '_', '.' or '/'", part)
				}
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	if len(out) > MaxTags {
		return nil, fmt.Errorf("%d tags; the limit is %d", len(out), MaxTags)
	}
	return out, nil
}

// CleanLine trims free text (an author, a reason, a line of evidence) to one
// line of at most max runes.
func CleanLine(s string, max int) string {
	s = oneLine(s)
	if r := []rune(s); len(r) > max {
		s = strings.TrimRightFunc(string(r[:max-1]), unicode.IsSpace) + "…"
	}
	return s
}

// WholeLine returns s when it is already one clean line of at most max runes,
// and nothing otherwise. It is for text a person may paste into a shell, such
// as the command that allows a check: that is given whole or not at all.
func WholeLine(s string, max int) string {
	if CleanLine(s, max) != s {
		return ""
	}
	return s
}

// Reorders reports whether r is one of the Unicode marks that change the
// order text is drawn in: the embeddings, overrides and isolates. With one of
// them a line can be made to read differently from what it says.
func Reorders(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// oneLine collapses all whitespace and drops control characters and marks that
// reorder text, so nothing an agent stores can break a terminal, a journal
// line or a Markdown list, or be drawn as something other than what it says.
func oneLine(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = true
		case unicode.IsControl(r) || r == 0x2028 || r == 0x2029 || r == 0xFEFF || Reorders(r):
			// dropped
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ValidID reports whether s looks like a fact id.
func ValidID(s string) bool {
	if !strings.HasPrefix(s, idPrefix) || len(s) != len(idPrefix)+idLength {
		return false
	}
	for _, r := range s[len(idPrefix):] {
		if !strings.ContainsRune(idAlphabet, r) {
			return false
		}
	}
	return true
}

// NewID makes a fact id from random bytes (at least idLength of them).
func NewID(random []byte) string {
	b := []byte(idPrefix)
	for i := 0; i < idLength; i++ {
		b = append(b, idAlphabet[int(random[i])%len(idAlphabet)])
	}
	return string(b)
}

// ParseDuration reads "90s", "15m", "26h", "30d" or "2w".
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if len(s) < 2 {
		return 0, fmt.Errorf("duration %q: write a number and a unit, like 26h or 30d", s)
	}
	unit := map[byte]time.Duration{'s': time.Second, 'm': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[s[len(s)-1]]
	if unit == 0 {
		return 0, fmt.Errorf("duration %q: the unit must be s, m, h, d or w", s)
	}
	var n int64
	for _, r := range s[:len(s)-1] {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("duration %q: write a whole number and a unit, like 26h or 30d", s)
		}
		n = n*10 + int64(r-'0')
		if time.Duration(n)*unit > maxDuration {
			return 0, fmt.Errorf("duration %q is too long", s)
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("duration %q must be more than zero", s)
	}
	return time.Duration(n) * unit, nil
}

// FormatDuration writes a duration the way ParseDuration reads it, in the
// largest unit that divides it exactly.
func FormatDuration(d time.Duration) string {
	for _, u := range []struct {
		d time.Duration
		s string
	}{{7 * 24 * time.Hour, "w"}, {24 * time.Hour, "d"}, {time.Hour, "h"}, {time.Minute, "m"}} {
		if d >= u.d && d%u.d == 0 {
			return fmt.Sprintf("%d%s", d/u.d, u.s)
		}
	}
	return fmt.Sprintf("%ds", int64(d/time.Second))
}

// Ago says how long ago t was, in the two-letter style the ledger uses.
func Ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 0:
		return "just now"
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

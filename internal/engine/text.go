package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
)

// The plain-text forms below are what an agent reads and what the command
// line prints. They are written to be unmistakable: the status comes first,
// in capitals, and the second line says what the status rests on.

// Text writes one fact as a short block.
func (v View) Text(now time.Time) string {
	var b strings.Builder
	label := strings.ToUpper(string(v.Status))
	if v.Retracted != nil {
		label = "RETRACTED"
	} else if v.ReplacedBy != "" {
		label = "REPLACED"
	} else if v.TimedOut() {
		// Its last result was a pass, but nothing was proved in this call,
		// and a reader who sees HOLDS takes it as proved just now.
		label = "TIMED OUT"
	}
	fmt.Fprintf(&b, "%-9s %s  %s\n", label, v.ID, v.Claim)
	pad := strings.Repeat(" ", 10)
	line := func(format string, a ...any) { fmt.Fprintf(&b, pad+format+"\n", a...) }

	switch {
	case v.Retracted != nil:
		who := ""
		if v.Retracted.By != "" {
			who = " by " + v.Retracted.By
		}
		line("retracted %s%s: %s", fact.Ago(now, v.Retracted.At), who, v.Retracted.Reason)
		return strings.TrimRight(b.String(), "\n")
	case v.ReplacedBy != "":
		line("replaced by %s", v.ReplacedBy)
		return strings.TrimRight(b.String(), "\n")
	}

	if v.TimedOut() {
		last := ""
		if v.CheckedAt != nil {
			last = " · it held when last checked " + fact.Ago(now, *v.CheckedAt)
		}
		line("not proved just now: the check did not finish in time%s", last)
		line("check: %s", v.Check)
		return strings.TrimRight(b.String(), "\n")
	}

	checked := ""
	if v.CheckedAt != nil {
		checked = " · checked " + fact.Ago(now, *v.CheckedAt)
		if v.Late {
			checked = " · last checked " + fact.Ago(now, *v.CheckedAt) + "; the check did not finish in time just now"
		}
	}
	switch v.Status {
	case fact.Holds:
		line("proof: %s%s", v.Detail, checked)
	case fact.Broken:
		held := ""
		if v.Held != nil {
			held = " · it last held " + fact.Ago(now, *v.Held)
		}
		line("proof failed: %s%s%s", v.Detail, checked, held)
		line("check: %s", v.Check)
	case fact.Blocked:
		line("not checked: %s", v.Detail)
		line("check: %s", v.Check)
	default:
		who := "a note"
		if v.By != "" {
			who += " by " + v.By
		}
		noted := v.Created
		if v.NotedAt != nil {
			noted = *v.NotedAt
		}
		if v.VouchedBy != "" && noted.After(v.Created) {
			who += ", confirmed by " + v.VouchedBy
		}
		if v.Status == fact.Stale {
			life := ""
			if v.StaleAt != nil {
				life = ", past its " + fact.FormatDuration(v.StaleAt.Sub(noted)) + " life"
			}
			line("%s, %s old%s; nothing proves it, so treat it as a guess", who, age(now.Sub(noted)), life)
		} else {
			line("%s, %s old; it has no check, so nothing proves it", who, age(now.Sub(noted)))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// age writes a duration the way a person would say how old something is.
func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// String sums a store up in one line, what needs attention first:
// "9 facts: 1 broken, 2 unproven, 6 hold".
func (c Counts) String() string {
	if c.Total == 0 {
		return "no facts"
	}
	var parts []string
	add := func(n int, one, many string) {
		switch {
		case n == 1:
			parts = append(parts, "1 "+one)
		case n > 1:
			parts = append(parts, fmt.Sprintf("%d %s", n, many))
		}
	}
	add(c.Broken, "broken", "broken")
	add(c.TimedOut, "timed out", "timed out")
	add(c.Blocked, "blocked", "blocked")
	add(c.Stale, "stale", "stale")
	add(c.Unproven, "unproven", "unproven")
	add(c.Holds, "holds", "hold")
	noun := "facts"
	if c.Total == 1 {
		noun = "fact"
	}
	return fmt.Sprintf("%d %s: %s", c.Total, noun, strings.Join(parts, ", "))
}

func blocks(views []View, now time.Time) string {
	parts := make([]string, len(views))
	for i, v := range views {
		parts[i] = v.Text(now)
	}
	return strings.Join(parts, "\n")
}

// advice tells an agent what the labels in front of it mean for its work.
// Only the lines that apply are included.
func advice(views []View) string {
	c := count(views)
	timedOut := false
	for _, v := range views {
		timedOut = timedOut || v.TimedOut()
	}
	var out []string
	if c.Broken > 0 {
		out = append(out, "BROKEN: this was true once and is not now. Do not rely on it. If you find out what is true now, remember that with replaces set to the old id.")
	}
	if timedOut {
		out = append(out, "TIMED OUT: the check did not finish, so this is as last known and is not proved. Run verify on it again, or confirm it yourself before relying on it.")
	}
	if c.Blocked > 0 {
		out = append(out, "BLOCKED: the check was not run, so this is neither proved nor disproved. Only a person can allow the check.")
	}
	if c.Stale > 0 || c.Unproven > 0 {
		out = append(out, "UNPROVEN and STALE: notes with no check. Confirm them yourself before relying on them.")
	}
	return strings.Join(out, "\n")
}

func join(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "\n\n")
}

// Text writes the answer to a recall.
func (r Recalled) Text(now time.Time) string {
	if len(r.Facts) == 0 {
		if r.Known == 0 {
			return "Nothing is remembered here yet."
		}
		if r.Known == 1 {
			return "Nothing remembered matches that. (1 fact is stored; recall with an empty query lists it.)"
		}
		return fmt.Sprintf("Nothing remembered matches that. (%d facts are stored; recall with an empty query lists them.)", r.Known)
	}
	c := count(r.Facts)
	head := c.String()
	if r.Matched > len(r.Facts) {
		head += fmt.Sprintf(" (showing %d of %d that match)", len(r.Facts), r.Matched)
	}
	head += "."
	// The timed out ones are in the count already. A fact that did not hold
	// before and could not be checked again keeps its label, so it is said here.
	if c.Late > c.TimedOut {
		head += fmt.Sprintf(" %d could not be checked again in time.", c.Late)
	}
	return join(head, blocks(r.Facts, now), advice(r.Facts), r.Note)
}

// Text writes the answer to a verify.
func (v Verified) Text(now time.Time) string {
	if len(v.Facts) == 0 {
		return "There are no facts with checks to prove."
	}
	head := "Proved again just now."
	if n := v.Counts.Late; n == 1 {
		head = "Not everything could be proved again: 1 check did not finish in time."
	} else if n > 1 {
		head = fmt.Sprintf("Not everything could be proved again: %d checks did not finish in time.", n)
	}
	return join(head+" "+v.Counts.String()+".", blocks(v.Facts, now), advice(v.Facts), v.Note)
}

// Text writes a list.
func (l Listed) Text(now time.Time) string {
	if len(l.Facts) == 0 && len(l.Gone) == 0 {
		return "Nothing is remembered here yet."
	}
	gone := ""
	if len(l.Gone) > 0 {
		gone = "Withdrawn:\n" + blocks(l.Gone, now)
	}
	return join(l.Counts.String()+".", blocks(l.Facts, now), gone, l.Note)
}

// Markdown writes the store as a document a person or an agent can read at
// the start of a session: what is wrong first, then what is known to hold.
func (l Listed) Markdown(now time.Time) string {
	var b strings.Builder
	b.WriteString("# What Holdfast knows\n\n")
	fmt.Fprintf(&b, "%s. Statuses are as of each fact's last check; run `holdfast check` to prove them again.\n", l.Counts)
	section := func(title string, timedOut bool, want ...fact.Status) {
		first := true
		for _, v := range l.Facts {
			match := false
			for _, s := range want {
				match = match || v.Status == s
			}
			if !match || v.TimedOut() != timedOut {
				continue
			}
			if first {
				fmt.Fprintf(&b, "\n## %s\n\n", title)
				first = false
			}
			fmt.Fprintf(&b, "- **%s** (`%s`)", v.Claim, v.ID)
			switch {
			case v.TimedOut():
				fmt.Fprintf(&b, "  \n  not proved just now: the check did not finish in time. When it last ran: %s", v.Detail)
			case v.Status == fact.Holds:
				fmt.Fprintf(&b, "  \n  proof: %s", v.Detail)
			case v.Status == fact.Broken:
				fmt.Fprintf(&b, "  \n  proof failed: %s", v.Detail)
			case v.Status == fact.Blocked:
				fmt.Fprintf(&b, "  \n  not checked: %s", v.Detail)
			default:
				noted := v.Created
				if v.NotedAt != nil {
					noted = *v.NotedAt
				}
				who := ""
				if v.By != "" {
					who = " by " + v.By
				}
				fmt.Fprintf(&b, "  \n  a note%s, %s old, with no check", who, age(now.Sub(noted)))
			}
			b.WriteString("\n")
		}
	}
	section("No longer true", false, fact.Broken)
	section("Timed out", true, fact.Holds)
	section("Could not be checked", false, fact.Blocked)
	section("Notes nobody has proved", false, fact.Stale, fact.Unproven)
	section("Holds", false, fact.Holds)
	return b.String()
}

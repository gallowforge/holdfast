package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gallowforge/holdfast/internal/fact"
)

const (
	unapproved = "the policy file has changed and nobody has approved it on this machine, so only the defaults apply. A person can read .holdfast/policy and approve it: holdfast trust"
	mistaken   = `the policy file has a mistake (policy line 2: unknown word "nonsense" (expected files, net or cmd)), so no check may touch anything until it is fixed`
	approvedIt = "Approved on this machine. A later change to the file will need approving again.\n"
)

// policyFile is where the project's policy is kept.
func (d *desk) policyFile() string { return filepath.Join(d.cwd, ".holdfast", "policy") }

// edit changes the policy the way a person with an editor would, or the way
// a pull from a colleague would: without telling Holdfast.
func (d *desk) edit(text string) {
	d.t.Helper()
	if err := os.WriteFile(d.policyFile(), []byte(text), 0o644); err != nil {
		d.t.Fatal(err)
	}
}

// inForce says whether the policy on disk is the one checks go by.
func (d *desk) inForce(more ...string) bool {
	d.t.Helper()
	got := d.run(append([]string{"policy"}, more...)...)
	switch {
	case got.code == 0 && strings.HasSuffix(got.out, "\nIn force: approved on this machine.\n"):
		return true
	case got.code == 1 && strings.Contains(got.out, "\nNot in force: "):
		return false
	}
	d.t.Fatalf("policy: exit %d\n%s%s", got.code, got.out, got.err)
	return false
}

// --- policy ---

func TestPolicyShowsTheFileAndSaysItIsInForce(t *testing.T) {
	d := project(t)
	text := read(t, d.policyFile())
	got := d.ok("policy")
	want := d.policyFile() + "\n\n" + strings.TrimRight(text, "\n") + "\n\nIn force: approved on this machine.\n"
	if got.out != want {
		t.Errorf("policy said:\n%s\nwant:\n%s", got.out, want)
	}
	// A new project's checks may read the project and reach this machine,
	// and nothing else; the file explains itself to whoever opens it.
	var rules []string
	for _, line := range strings.Split(text, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			rules = append(rules, line)
		}
	}
	if strings.Join(rules, "; ") != "files .; net loopback" {
		t.Errorf("a new project's policy allows %q, want files . and net loopback", rules)
	}
	says(t, "the policy file", text, "# Holdfast policy: what checks may touch.", "holdfast allow", "#   files <folder>", "#   net   <who>", "#   cmd   <program>")

	got = d.run("policy", "net")
	if got.code != 2 || got.out != "" || !strings.HasPrefix(got.err, "holdfast: policy takes no arguments; to widen it, use: holdfast allow\nUsage: holdfast policy") {
		t.Errorf("policy with an argument: exit %d, %q, %q", got.code, got.out, got.err)
	}
}

func TestPolicyExitsOneWhenTheFileIsNotInForce(t *testing.T) {
	s := stocked(t)
	text := read(t, s.policyFile())
	s.edit(text + "files " + s.outside + "\n")

	got := s.run("policy")
	// The person is shown the file as it is now: that is what they would approve.
	want := s.policyFile() + "\n\n" + strings.TrimRight(text, "\n") + "\nfiles " + s.outside + "\n\nNot in force: " + unapproved + "\n"
	if got.code != 1 || got.err != "" || got.out != want {
		t.Errorf("policy after a change by hand: exit %d, %q\n%s\nwant exit 1 and:\n%s", got.code, got.err, got.out, want)
	}

	// Putting the file back as it was approved is enough: nothing to approve.
	s.edit(text)
	if !s.inForce() {
		t.Error("the policy that was approved is not in force once the file is put back")
	}
}

func TestAPolicyChangedByHandWidensNothingUntilAPersonApprovesIt(t *testing.T) {
	s := stocked(t)
	s.edit(read(t, s.policyFile()) + "files " + s.outside + "\n")

	// Every answer that rests on the policy says that it is not the file's.
	for _, args := range [][]string{{"check"}, {"list"}, {"recall", "data"}, {"brief"}} {
		got := s.ok(args...)
		says(t, "holdfast "+args[0], got.out, "BLOCKED   "+s.blocked.ID, "\n"+unapproved+"\n")
	}
	// The defaults still apply, so the project's own files can be checked.
	if got := s.ok("check"); !strings.Contains(got.out, "HOLDS     "+s.proved.ID) {
		t.Errorf("under the defaults the project's own fact does not hold:\n%s", got.out)
	}
	// A new fact that needs the wider policy waits too.
	if v := s.add("The data file is there too", "file "+filepath.Join(s.outside, "data.txt")); v.Status != "blocked" {
		t.Errorf("a fact stored under an unapproved policy is %s, want blocked", v.Status)
	}
	if got := s.run("check", "--strict"); got.code != 1 {
		t.Errorf("check --strict: exit %d, want 1 while facts are blocked", got.code)
	}

	s.ok("trust", "--yes")
	got := s.ok("check", "--strict")
	says(t, "check after approval", got.out, "3 facts: 3 hold.", "HOLDS     "+s.blocked.ID)
	lacks(t, "check after approval", got.out, unapproved)
}

func TestAPolicyNarrowedByHandAlsoWaitsForApproval(t *testing.T) {
	d := project(t)
	v := d.add(goClaim, goCheck)
	d.edit("net loopback\n")
	if d.inForce() {
		t.Fatal("a policy changed by hand is in force without approval")
	}
	if got := d.ok("check"); !strings.Contains(got.out, "HOLDS     "+v.ID) {
		t.Errorf("before approval the defaults should still apply:\n%s", got.out)
	}
	got := d.ok("trust", "--yes")
	if want := d.policyFile() + " allows checks to:\n\n  connect to        loopback\n\n" + approvedIt; got.out != want {
		t.Errorf("trust said:\n%s\nwant:\n%s", got.out, want)
	}
	got = d.ok("check")
	says(t, "check under the narrowed policy", got.out, "1 fact: 1 blocked.", "BLOCKED   "+v.ID, "go.mod is outside the folders the policy allows")
	// Blocked is not broken: the fact still says when it last held.
	if now := d.why(v.ID); now.Status != "blocked" || now.Held == nil {
		t.Errorf("the fact is %s, last held %v", now.Status, now.Held)
	}
}

func TestAPolicyWithAMistakeLetsNoCheckRun(t *testing.T) {
	s := stocked(t)
	good := read(t, s.policyFile())
	s.edit("files .\nnonsense here\n")

	got := s.run("policy")
	if want := s.policyFile() + "\n\nfiles .\nnonsense here\n\nNot in force: " + mistaken + "\n"; got.code != 1 || got.out != want {
		t.Errorf("policy with a mistake: exit %d\n%s\nwant exit 1 and:\n%s", got.code, got.out, want)
	}
	// Not even the defaults: a half-read policy is no policy.
	got = s.ok("check")
	says(t, "check", got.out, "2 facts: 2 blocked.", "BLOCKED   "+s.proved.ID, "BLOCKED   "+s.blocked.ID, "\n"+mistaken+"\n")
	if got := s.run("check", "--strict"); got.code != 1 {
		t.Errorf("check --strict: exit %d, want 1", got.code)
	}
	if v := s.add("The module file exists", "file go.mod"); v.Status != "blocked" {
		t.Errorf("a fact stored under a policy with a mistake is %s, want blocked", v.Status)
	}

	// There is nothing to approve and nothing to widen until it is fixed.
	got = s.run("trust", "--yes")
	want := `holdfast: the policy file has a mistake, so there is nothing to approve: policy line 2: unknown word "nonsense" (expected files, net or cmd)` + "\n"
	if got.code != 2 || got.out != "" || got.err != want {
		t.Errorf("trust with a mistake in the file: exit %d, %q, %q; want exit 2 and %q", got.code, got.out, got.err, want)
	}
	if got := s.run("allow", "net", "example.com"); got.code != 1 || got.out != "" {
		t.Errorf("allow with a mistake in the file: exit %d, %q", got.code, got.out)
	}
	if text := read(t, s.policyFile()); text != "files .\nnonsense here\n" {
		t.Errorf("the file with the mistake was rewritten:\n%s", text)
	}

	s.edit(good)
	if got := s.ok("check"); !strings.Contains(got.out, "3 facts: 1 blocked, 2 hold.") {
		t.Errorf("check after the mistake was fixed said:\n%s", got.out)
	}
}

// --- trust ---

func TestTrustListsWhatThePolicyAllows(t *testing.T) {
	d := project(t)
	got := d.ok("trust")
	want := d.policyFile() + " allows checks to:\n\n  read files in     .\n  connect to        loopback\n\nIt is already approved on this machine.\n"
	if got.out != want {
		t.Errorf("trust said:\n%s\nwant:\n%s", got.out, want)
	}

	// Everything in the file is on the list, in plain words, before the question.
	d.edit("# ours\nfiles . docs\nnet loopback staging.example.com\ncmd git status --short\ncmd go version\n")
	d.prompt, d.in = true, "n\n"
	got = d.run("trust")
	want = d.policyFile() + " allows checks to:\n\n" +
		"  read files in     .\n  read files in     docs\n" +
		"  connect to        loopback\n  connect to        staging.example.com\n" +
		"  run the command   git status --short\n  run the command   go version\n\n" +
		"Approve this on this machine? [y/N] Not approved. Only the defaults apply.\n"
	if got.code != 1 || got.err != "" || got.out != want {
		t.Errorf("trust, answered no: exit %d, %q\n%s\nwant exit 1 and:\n%s", got.code, got.err, got.out, want)
	}

	// A policy that allows nothing says so, instead of an empty list.
	d.edit("# nothing\n")
	got = d.ok("trust", "--yes")
	if want := d.policyFile() + " allows checks to:\n\n  nothing at all\n\n" + approvedIt; got.out != want {
		t.Errorf("trust of an empty policy said:\n%s\nwant:\n%s", got.out, want)
	}

	got = d.run("trust", "me")
	if got.code != 2 || !strings.HasPrefix(got.err, "holdfast: trust takes no arguments\nUsage: holdfast trust") {
		t.Errorf("trust with an argument: exit %d, %q", got.code, got.err)
	}
}

func TestTrustWithNobodyAtTheKeyboardApprovesNothing(t *testing.T) {
	s := stocked(t)
	s.edit(read(t, s.policyFile()) + "files " + s.outside + "\n")
	// Whatever is piped in, an agent or a script cannot answer the question
	// for the person.
	for _, in := range []string{"", "y\n", "yes\n"} {
		s.in = in
		got := s.run("trust")
		if got.code != 1 || got.err != "holdfast: not approved. Read the list above, then run this in a terminal, or pass --yes.\n" {
			t.Errorf("trust with %q piped in: exit %d, %q; want exit 1", in, got.code, got.err)
		}
		says(t, "trust", got.out, "  read files in     "+s.outside+"\n")
		lacks(t, "trust", got.out, "Approve this", "Approved")
		if s.inForce() {
			t.Fatalf("with %q piped in, the policy was approved without a person", in)
		}
	}
	if got := s.ok("check"); !strings.Contains(got.out, "BLOCKED   "+s.blocked.ID) {
		t.Errorf("the fact that needs the wider policy is no longer blocked:\n%s", got.out)
	}
}

func TestTrustYesApprovesThePolicyAsItIsNow(t *testing.T) {
	s := stocked(t)
	s.edit(read(t, s.policyFile()) + "files " + s.outside + "\n")
	got := s.ok("trust", "--yes")
	if !strings.HasSuffix(got.out, "\n\n"+approvedIt) {
		t.Errorf("trust --yes said:\n%s", got.out)
	}
	if !s.inForce() {
		t.Fatal("after trust --yes the policy is not in force")
	}
	if got := s.ok("trust"); !strings.HasSuffix(got.out, "\n\nIt is already approved on this machine.\n") {
		t.Errorf("a second trust said:\n%s", got.out)
	}

	// The approval is of that text: the next change needs its own.
	s.edit(read(t, s.policyFile()) + "net public\n")
	if s.inForce() {
		t.Error("a later change to the file is in force without being approved")
	}
	if got := s.run("trust"); got.code != 1 {
		t.Errorf("trust of the later change with nobody at the keyboard: exit %d", got.code)
	}
}

func TestTrustTakesOnlyYesForAnAnswer(t *testing.T) {
	for name, c := range map[string]struct {
		in  string
		yes bool
	}{
		"y":                     {"y\n", true},
		"a capital Y":           {"Y\n", true},
		"yes":                   {"yes\n", true},
		"yes, loudly":           {"  YES \n", true},
		"y and no new line":     {"y", true},
		"n":                     {"n\n", false},
		"just the enter key":    {"\n", false},
		"nothing: input closed": {"", false},
		"another word":          {"yep\n", false},
		"no, then yes":          {"no\ny\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			d := project(t)
			d.edit("files .\nnet loopback public\n")
			d.prompt, d.in = true, c.in
			got := d.run("trust")
			says(t, "trust", got.out, "  connect to        public\n\nApprove this on this machine? [y/N] ")
			if c.yes {
				if got.code != 0 || got.err != "" || !strings.HasSuffix(got.out, "[y/N] "+approvedIt) {
					t.Errorf("exit %d, %q\n%s", got.code, got.err, got.out)
				}
			} else if got.code != 1 || got.err != "" || !strings.HasSuffix(got.out, "[y/N] Not approved. Only the defaults apply.\n") {
				t.Errorf("exit %d, %q\n%s", got.code, got.err, got.out)
			}
			if d.inForce() != c.yes {
				t.Errorf("answered %q: is the policy in force? %v", c.in, !c.yes)
			}
		})
	}
}

// What one machine approved says nothing about another: the file travels
// with the project, the approval does not.
func TestAnApprovalIsForThisMachineOnly(t *testing.T) {
	s := stocked(t)
	s.ok("allow", "files", s.outside)
	if !s.inForce() {
		t.Fatal("the policy just widened here is not in force here")
	}

	here := s.state
	t.Setenv("HOLDFAST_STATE", folder(t))
	if s.inForce() {
		t.Fatal("a policy approved on one machine is in force on another")
	}
	if got := s.ok("check"); !strings.Contains(got.out, "BLOCKED   "+s.blocked.ID) || !strings.Contains(got.out, unapproved) {
		t.Errorf("on the other machine the wider policy is used unapproved:\n%s", got.out)
	}
	s.ok("trust", "--yes")
	if got := s.ok("check"); !strings.Contains(got.out, "HOLDS     "+s.blocked.ID) {
		t.Errorf("after approval on the other machine:\n%s", got.out)
	}

	t.Setenv("HOLDFAST_STATE", here)
	if !s.inForce() {
		t.Error("approving on another machine undid the approval here")
	}
}

// --- allow ---

func TestAllowWidensThePolicyAndProvesWhatWasWaiting(t *testing.T) {
	s := stocked(t)
	before := read(t, s.policyFile())
	got := s.ok("allow", "files", s.outside)
	file := filepath.Join(s.outside, "data.txt")
	want := "Allowed: files " + s.outside + "\n\nThe facts that were blocked, checked again:\n" +
		"HOLDS     " + s.blocked.ID + "  The data is there\n" + pad + "proof: " + fact.CleanLine(file+" exists", fact.MaxDetail) + " · checked just now\n"
	if calm(got.out) != want {
		t.Errorf("allow said:\n%s\nwant:\n%s", got.out, want)
	}
	// The rule is one more line; what a person wrote above it is kept.
	if after := read(t, s.policyFile()); after != before+"files "+s.outside+"\n" {
		t.Errorf("the policy file now reads:\n%s", after)
	}
	// It was a person who ran this, so there is nothing left to approve.
	if !s.inForce() {
		t.Error("the policy is not in force after allow")
	}
	if got := s.ok("check", "--strict"); !strings.Contains(got.out, "2 facts: 2 hold.") {
		t.Errorf("check after allow said:\n%s", got.out)
	}

	// With nothing waiting there is one line to say.
	if got := s.ok("allow", "net", "staging.example.com", "*.example.org", "10.0.0.0/8"); got.out != "Allowed: net staging.example.com *.example.org 10.0.0.0/8\n" {
		t.Errorf("allow with nothing waiting said %q", got.out)
	}
	says(t, "trust", s.ok("trust").out,
		"  read files in     "+s.outside+"\n",
		"  connect to        staging.example.com\n  connect to        *.example.org\n  connect to        10.0.0.0/8\n")
}

// A fact that still cannot be checked after the policy grew is shown as it
// is, not as proved.
func TestAllowShowsFactsThatAreStillBlocked(t *testing.T) {
	s := stocked(t)
	got := s.ok("allow", "files", "docs")
	want := "Allowed: files docs\n\nThe facts that were blocked, checked again:\n" + s.blockedText() + "\n"
	if got.out != want {
		t.Errorf("allow said:\n%s\nwant:\n%s", got.out, want)
	}
}

func TestAllowKeepsACommandRuleWordForWord(t *testing.T) {
	for name, c := range map[string]struct {
		rule []string
		line string
	}{
		"plain words":             {[]string{"git", "status", "--short"}, "cmd git status --short"},
		"dashes of its own":       {[]string{"ls", "-la", "--dir", "/x"}, "cmd ls -la --dir /x"},
		"words holdfast also has": {[]string{"npm", "ls", "--global", "--json"}, "cmd npm ls --global --json"},
		"a help flag":             {[]string{"git", "-h"}, "cmd git -h"},
		"an equals sign":          {[]string{"git", "log", "-1", "--format=%H"}, "cmd git log -1 --format=%H"},
		"stars for any argument":  {[]string{"go", "test", "*", "**"}, "cmd go test * **"},
		"words with spaces":       {[]string{"echo", "two words", "it's"}, `cmd echo "two words" "it's"`},
		"an empty argument":       {[]string{"printf", ""}, `cmd printf ""`},
	} {
		t.Run(name, func(t *testing.T) {
			d := project(t)
			before := read(t, d.policyFile())
			got := d.ok(append([]string{"allow", "cmd"}, c.rule...)...)
			if got.out != "Allowed: "+c.line+"\n" {
				t.Errorf("allow said %q, want %q", got.out, "Allowed: "+c.line+"\n")
			}
			if after := read(t, d.policyFile()); after != before+c.line+"\n" {
				t.Errorf("the policy file ends:\n%s", strings.TrimPrefix(after, before))
			}
			// The file still reads as a policy, and as the same command.
			says(t, "trust", d.ok("trust").out, "  run the command   "+strings.Join(c.rule, " ")+"\n", "It is already approved on this machine.")
			// None of it was taken as a flag of holdfast's own.
			if left := tree(t, d.home); len(left) != 0 {
				t.Errorf("the rule reached the store for the whole machine: %v", left)
			}
		})
	}
}

func TestAllowSaysWhenARuleIsAlreadyThere(t *testing.T) {
	d := project(t)
	d.ok("allow", "net", "staging.example.com")
	d.ok("allow", "cmd", "git", "status", "--short")
	before := read(t, d.policyFile())
	for line, args := range map[string][]string{
		"net staging.example.com": {"allow", "net", "staging.example.com"},
		"cmd git status --short":  {"allow", "cmd", "git", "status", "--short"},
		"files .":                 {"allow", "files", "."},
		"net loopback":            {"allow", "net", "loopback"},
	} {
		if got := d.ok(args...); got.out != "Already allowed: "+line+"\n" {
			t.Errorf("holdfast %v said %q", args, got.out)
		}
	}
	if after := read(t, d.policyFile()); after != before {
		t.Errorf("a rule that was already there was written again:\n%s", after)
	}

	// A file a person wrote may end without a new line; its last rule is
	// still a rule, and the next one goes on a line of its own.
	d.edit("files .\nnet loopback\nnet api.example.com")
	d.ok("trust", "--yes")
	if got := d.ok("allow", "net", "api.example.com"); got.out != "Already allowed: net api.example.com\n" {
		t.Errorf("allow of the file's last rule said %q", got.out)
	}
	if text := read(t, d.policyFile()); text != "files .\nnet loopback\nnet api.example.com" {
		t.Errorf("the file was rewritten:\n%q", text)
	}
	d.ok("allow", "net", "db.example.com")
	if text := read(t, d.policyFile()); text != "files .\nnet loopback\nnet api.example.com\nnet db.example.com\n" {
		t.Errorf("the file now reads:\n%q", text)
	}
}

func TestAllowRefusesWhatAPolicyCannotSay(t *testing.T) {
	s := stocked(t)
	before := read(t, s.policyFile())
	for name, c := range map[string]struct {
		args  []string
		why   string
		usage bool
	}{
		"nothing":                     {[]string{"allow"}, "holdfast: say what to allow\n", true},
		"a kind and no rule":          {[]string{"allow", "net"}, "holdfast: say what to allow\n", true},
		"a kind nobody knows":         {[]string{"allow", "bogus", "x"}, `holdfast: "bogus" is not something a policy allows; write net, cmd or files` + "\n", true},
		"a kind in capitals":          {[]string{"allow", "NET", "example.com"}, `holdfast: "NET" is not something a policy allows; write net, cmd or files` + "\n", true},
		"a rule as one word":          {[]string{"allow", "net example.com"}, "holdfast: say what to allow\n", true},
		"a net rule it cannot read":   {[]string{"allow", "net", "bad rule!"}, `holdfast: net rule "bad rule!": write loopback, private, public, a host name, *.a-domain, an address or a range` + "\n", false},
		"one bad net rule among good": {[]string{"allow", "net", "example.com", "bad rule!"}, `holdfast: net rule "bad rule!"`, false},
		"an empty net rule":           {[]string{"allow", "net", ""}, "holdfast: an empty net rule\n", false},
		// An empty folder name would stand for the store's whole folder.
		"an empty folder name":         {[]string{"allow", "files", ""}, "holdfast: an empty files rule: to allow the store's whole folder, write: files .\n", false},
		"an empty folder among others": {[]string{"allow", "files", s.outside, ""}, "holdfast: an empty files rule", false},
		"a program with no name":       {[]string{"allow", "cmd", "", "status"}, "holdfast: an empty cmd rule: the program has no name\n", false},
		// A second line smuggled in would be a second rule.
		"a folder name with a new line": {[]string{"allow", "files", "docs\nnet public"}, "holdfast: policy line ", false},
		"a net rule with a new line":    {[]string{"allow", "net", "example.com\nnet public"}, "holdfast: net rule ", false},
		// Every other command takes flags anywhere; here they would become the rule.
		"a flag after a net rule":   {[]string{"allow", "net", "example.com", "--global"}, `holdfast: "--global" would become part of the rule; flags go before "net"` + "\n", true},
		"a flag after a folder":     {[]string{"allow", "files", s.outside, "--dir", s.outside}, `holdfast: "--dir" would become part of the rule; flags go before "files"` + "\n", true},
		"a flag in place of a rule": {[]string{"allow", "net", "-h"}, `holdfast: "-h" would become part of the rule; flags go before "net"` + "\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			got := s.run(c.args...)
			if got.code != 2 || got.out != "" || !strings.HasPrefix(got.err, c.why) {
				t.Errorf("exit %d, %q, %q; want exit 2 and %q", got.code, got.out, got.err, c.why)
			}
			if strings.Contains(got.err, "Usage: holdfast allow") != c.usage {
				t.Errorf("is the usage shown? %v, want %v:\n%s", !c.usage, c.usage, got.err)
			}
			if after := read(t, s.policyFile()); after != before {
				t.Errorf("a refused rule changed the policy file:\n%s", strings.TrimPrefix(after, before))
			}
		})
	}
	if !s.inForce() {
		t.Error("refused rules left the policy not in force")
	}
	if got := s.ok("check"); !strings.Contains(got.out, "BLOCKED   "+s.blocked.ID) {
		t.Errorf("a refused rule unblocked a fact:\n%s", got.out)
	}
}

func TestAllowWillNotWidenAPolicyNobodyApproved(t *testing.T) {
	s := stocked(t)
	// A change arrives from elsewhere: a pull, an editor, an agent's write.
	changed := read(t, s.policyFile()) + "net public\n"
	s.edit(changed)

	got := s.run("allow", "files", s.outside)
	want := "holdfast: the policy file has changed since it was last approved on this machine, so it is not widened blind.\n" +
		`Read it with "holdfast policy", approve it with "holdfast trust", then allow more.` + "\n"
	if got.code != 1 || got.out != "" || got.err != want {
		t.Errorf("allow on an unapproved policy: exit %d, %q, %q; want exit 1 and %q", got.code, got.out, got.err, want)
	}
	// Allowing one more thing must not approve the rest by the way.
	if text := read(t, s.policyFile()); text != changed {
		t.Errorf("the file was rewritten:\n%s", text)
	}
	if s.inForce() {
		t.Fatal("a refused allow approved the change it found")
	}

	s.ok("trust", "--yes")
	got = s.ok("allow", "files", s.outside)
	says(t, "allow after approval", got.out, "Allowed: files "+s.outside+"\n", "HOLDS     "+s.blocked.ID)
	if text := read(t, s.policyFile()); text != changed+"files "+s.outside+"\n" {
		t.Errorf("the file now reads:\n%s", text)
	}
}

func TestAllowTakesItsOwnFlagsBeforeTheKind(t *testing.T) {
	d := project(t)
	here := read(t, d.policyFile())
	other := folder(t)
	d.ok("init", "--dir", other)
	there := read(t, filepath.Join(other, ".holdfast", "policy"))

	if got := d.ok("allow", "--dir", other, "net", "staging.example.com"); got.out != "Allowed: net staging.example.com\n" {
		t.Errorf("allow --dir said %q", got.out)
	}
	if text := read(t, filepath.Join(other, ".holdfast", "policy")); text != there+"net staging.example.com\n" {
		t.Errorf("the other project's policy reads:\n%s", text)
	}
	if text := read(t, d.policyFile()); text != here {
		t.Errorf("allow --dir changed this project's policy:\n%s", text)
	}

	for _, flag := range []string{"-h", "--help"} {
		got := d.run("allow", flag)
		if got.code != 0 || got.out != "" || !strings.HasPrefix(got.err, "Usage: holdfast allow net <who>... | cmd <program> [<argument>...] | files <folder>...") {
			t.Errorf("allow %s: exit %d, %q, %q", flag, got.code, got.out, got.err)
		}
	}
	// Two dashes end the flags, as everywhere.
	if got := d.ok("allow", "--", "cmd", "true"); got.out != "Allowed: cmd true\n" {
		t.Errorf("allow -- cmd true said %q", got.out)
	}
}

// --- the store for the whole machine ---

func TestTheMachineStoreStartsWithNoFolderToRead(t *testing.T) {
	d := sit(t)
	d.write(filepath.Join(d.home, "notes.txt"), "remember the milk\n")
	path := filepath.Join(d.home, ".holdfast")

	got := d.ok("init", "--global")
	says(t, "init --global", got.out, "Created "+path+"\n",
		"the facts about this machine, for every project on it.",
		"no\n                   files until you name a folder (holdfast allow files ~/notes).",
		`holdfast add --global "The API listens on port 8080"`, "holdfast mcp --global ", "holdfast serve --global ")
	lacks(t, "init --global", got.out, "Commit it")
	if left := tree(t, d.cwd); len(left) != 0 {
		t.Errorf("init --global wrote %v in the current folder", left)
	}
	if got := d.ok("init", "--global"); got.out != "Already set up: "+path+"\n" {
		t.Errorf("a second init --global said %q", got.out)
	}

	// The home folder holds keys, tokens and mail: no check reads it until a
	// person names the folders that matter.
	policy := d.ok("policy", "--global")
	says(t, "policy --global", policy.out, filepath.Join(path, "policy")+"\n", "\nnet loopback\n", "In force: approved on this machine.")
	lacks(t, "policy --global", policy.out, "\nfiles ")
	if got := d.ok("trust", "--global"); !strings.Contains(got.out, "allows checks to:\n\n  connect to        loopback\n\nIt is already approved") {
		t.Errorf("trust --global said:\n%s", got.out)
	}

	got = d.ok("add", "--global", "My notes exist", "--check", "file notes.txt")
	says(t, "add --global", got.out, "Stored, but not proved: its check was not run.\nBLOCKED   ",
		"not checked: notes.txt is outside the folders the policy allows.", pad+"check: file notes.txt\n")
	if got := d.run("check", "--global", "--strict"); got.code != 1 || !strings.Contains(got.out, "1 fact: 1 blocked.") {
		t.Errorf("check --global --strict: exit %d\n%s", got.code, got.out)
	}
	// A note needs no policy at all.
	if v := d.add("The router is at 192.168.1.1", "", "--global"); v.Status != "unproven" {
		t.Errorf("a note in the machine store is %s", v.Status)
	}
}

func TestAllowGlobalNamesTheFoldersChecksMayRead(t *testing.T) {
	d := sit(t)
	d.write(filepath.Join(d.home, "notes", "todo.txt"), "x\n")
	d.write(filepath.Join(d.home, ".ssh", "id_ed25519"), "not a real key\n")
	d.ok("init", "--global")
	todo := d.add("There is a to-do list", "file notes/todo.txt", "--global")
	key := d.add("There is an SSH key", "file .ssh/id_ed25519", "--global")
	if todo.Status != "blocked" || key.Status != "blocked" {
		t.Fatalf("before any folder is named the facts are %s and %s, want both blocked", todo.Status, key.Status)
	}

	got := d.ok("allow", "--global", "files", "notes")
	says(t, "allow --global", calm(got.out), "Allowed: files notes\n\nThe facts that were blocked, checked again:\n",
		"HOLDS     "+todo.ID+"  There is a to-do list\n"+pad+"proof: notes/todo.txt exists · checked just now",
		"BLOCKED   "+key.ID+"  There is an SSH key\n"+pad+"not checked: .ssh/id_ed25519 is outside the folders the policy allows.")
	says(t, "the machine's policy", read(t, filepath.Join(d.home, ".holdfast", "policy")), "\nnet loopback\nfiles notes\n")

	// An empty name, as from a shell variable that was never set, must not
	// open the whole home folder.
	before := read(t, filepath.Join(d.home, ".holdfast", "policy"))
	empty := d.run("allow", "--global", "files", "")
	if empty.code != 2 || empty.out != "" || !strings.HasPrefix(empty.err, "holdfast: an empty files rule") {
		t.Errorf("allow --global files with an empty name: exit %d, %q, %q; want exit 2", empty.code, empty.out, empty.err)
	}
	if after := read(t, filepath.Join(d.home, ".holdfast", "policy")); after != before {
		t.Errorf("the empty rule was written:\n%s", after)
	}
	if v := d.why(key.ID, "--global"); v.Status != "blocked" {
		t.Errorf("after an empty files rule the key can be read: the fact is %s", v.Status)
	}
}

func TestTheMachineStoreAndAProjectStoreAreKeptApart(t *testing.T) {
	d := project(t)
	d.ok("init", "--global")
	mine := d.add(goClaim, goCheck)
	machine := d.add("The router is at 192.168.1.1", "", "--global")

	if l := d.listed(); l.Counts.Total != 1 || l.Facts[0].ID != mine.ID {
		t.Errorf("the project's store holds %+v", l.Counts)
	}
	if l := d.listed("--global"); l.Counts.Total != 1 || l.Facts[0].ID != machine.ID {
		t.Errorf("the machine's store holds %+v", l.Counts)
	}
	// A fact is known by its id in its own store only.
	if got := d.run("why", machine.ID); got.code != 1 {
		t.Errorf("the project's store answers for the machine's fact: exit %d", got.code)
	}
	if got := d.run("retract", mine.ID, "--global", "--reason", "wrong store"); got.code != 1 {
		t.Errorf("the machine's store retracted the project's fact: exit %d", got.code)
	}
	// Widening one policy leaves the other alone.
	before := read(t, d.policyFile())
	d.ok("allow", "--global", "net", "private")
	if after := read(t, d.policyFile()); after != before {
		t.Errorf("allow --global changed the project's policy:\n%s", after)
	}
	lacks(t, "the project's policy", d.ok("policy").out, "\nnet private")
	says(t, "the machine's policy", d.ok("policy", "--global").out, "\nnet private\n")
}

func TestAFolderUnderHomeWithNoStoreOfItsOwnUsesTheMachineStore(t *testing.T) {
	d := sit(t)
	d.ok("init", "--global")
	machine := d.add("The router is at 192.168.1.1", "", "--global")

	d.cwd = filepath.Join(d.home, "projects", "atlas")
	d.write("go.mod", goMod)
	if l := d.listed(); l.Counts.Total != 1 || l.Facts[0].ID != machine.ID {
		t.Fatalf("from a folder under home the store holds %+v, want the machine's one fact", l.Counts)
	}
	// It is the machine's store still, with the machine's policy: being
	// found from inside a project does not let it read the project.
	v := d.add("The module file exists", "file projects/atlas/go.mod")
	if v.Status != "blocked" {
		t.Errorf("the machine store read a file with no folder allowed: %s: %s", v.Status, v.Detail)
	}

	// Once the project has a store of its own, that is the one used.
	got := d.ok("init")
	says(t, "init", got.out, "Created "+filepath.Join(d.cwd, ".holdfast")+"\n", "Commit it")
	if l := d.listed(); l.Counts.Total != 0 {
		t.Errorf("the project's new store holds %d facts", l.Counts.Total)
	}
	if v := d.add("The module file exists", "file go.mod"); v.Status != "holds" {
		t.Errorf("in the project's own store the fact is %s: %s", v.Status, v.Detail)
	}
	if l := d.listed("--global"); l.Counts.Total != 2 {
		t.Errorf("the machine's store holds %d facts, want its 2", l.Counts.Total)
	}
}

// A store made in the home folder without --global is the same store, and
// gets the same careful start.
func TestInitInTheHomeFolderIsTheMachineStore(t *testing.T) {
	d := sit(t)
	d.cwd = d.home
	d.write("notes.txt", "x\n")
	got := d.ok("init")
	says(t, "init in the home folder", got.out, "Created "+filepath.Join(d.home, ".holdfast")+"\n", "the facts about this machine")
	policy := d.ok("policy")
	lacks(t, "the policy", policy.out, "\nfiles ")
	if v := d.add("My notes exist", "file notes.txt"); v.Status != "blocked" {
		t.Errorf("a file in the home folder was read with no folder allowed: %s", v.Status)
	}
	if l := d.listed("--global"); l.Counts.Total != 1 {
		t.Errorf("--global does not find the store made in the home folder: %+v", l.Counts)
	}
}

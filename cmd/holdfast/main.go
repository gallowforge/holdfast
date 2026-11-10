// Command holdfast is a memory for coding agents in which every fact carries
// its own proof.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gallowforge/holdfast/internal/api"
	"github.com/gallowforge/holdfast/internal/demo"
	"github.com/gallowforge/holdfast/internal/engine"
	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/mcp"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/serve"
	"github.com/gallowforge/holdfast/internal/store"
	"github.com/gallowforge/holdfast/internal/web"
	"github.com/gallowforge/holdfast/internal/world"
)

// version is set by the build.
var version = "dev"

const usage = `holdfast: a memory for coding agents, where every fact carries its own proof.

Usage: holdfast <command> [arguments]        (holdfast <command> -h for its flags)

Set up
  init                   create a .holdfast folder in this project
                         (--global: one for the whole machine, in your home folder)
  mcp                    serve the memory to an agent over stdio (Model Context Protocol)
  serve                  open the ledger in a browser (--demo: a pretend project to try it on)

Facts
  add "<claim>"          remember a fact; --check "<check>" makes it prove itself
  list                   every fact as last known
  check [<id>...]        prove facts again now; exits 1 if any of them is broken
                         or could not be checked
  recall "<question>"    what an agent gets when it asks
  why <id>               one fact, with its check and its history
  vouch <id>             confirm a note that has no check, by hand
  retract <id>           withdraw a fact (--reason "<why>")
  brief                  a short summary to give an agent when a session starts
  export                 the whole memory as Markdown

Trust
  policy                 show what checks may touch
  allow net|cmd|files    widen it, for example: holdfast allow net staging.example.com
  trust                  approve a policy file that someone else wrote

Other
  compact                shrink the journal of evidence
  version

Checks
  ` + "%s" + `

Every command takes --dir <folder> (or HOLDFAST_DIR) to name the project, and
--global for the store in your home folder. Without either, the nearest
.holdfast folder at or above the current folder is used.
`

type app struct {
	in       io.Reader
	out, err io.Writer
	cwd      string
	env      func(string) string
	color    bool
	prompt   bool // whether a person is at the keyboard
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "holdfast:", err)
		os.Exit(2)
	}
	a := &app{in: os.Stdin, out: os.Stdout, err: os.Stderr, cwd: cwd, env: os.Getenv}
	a.color = terminal(os.Stdout) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	a.prompt = terminal(os.Stdin) && terminal(os.Stdout)
	os.Exit(a.run(os.Args[1:]))
}

func terminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// Exit codes: 0 fine; 1 the answer is no (a fact is broken or could not be
// checked, a request was refused); 2 the command could not be carried out.
func (a *app) run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(a.out, usage, strings.ReplaceAll(fact.Grammar, "\n", "\n  "))
		return 0
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprintf(a.out, usage, strings.ReplaceAll(fact.Grammar, "\n", "\n  "))
		return 0
	case "version", "--version":
		fmt.Fprintln(a.out, "holdfast", version)
		return 0
	case "init":
		return a.cmdInit(rest)
	case "add", "remember":
		return a.cmdAdd(rest)
	case "list", "ls":
		return a.cmdList(rest)
	case "check", "verify":
		return a.cmdCheck(rest)
	case "recall":
		return a.cmdRecall(rest)
	case "why", "show":
		return a.cmdWhy(rest)
	case "vouch":
		return a.cmdVouch(rest)
	case "retract":
		return a.cmdRetract(rest)
	case "brief":
		return a.cmdBrief(rest)
	case "export":
		return a.cmdExport(rest)
	case "policy":
		return a.cmdPolicy(rest)
	case "allow":
		return a.cmdAllow(rest)
	case "trust":
		return a.cmdTrust(rest)
	case "compact":
		return a.cmdCompact(rest)
	case "mcp":
		return a.cmdMCP(rest)
	case "serve":
		return a.cmdServe(rest)
	}
	fmt.Fprintf(a.err, "holdfast: there is no command %q. Run \"holdfast help\" for the list.\n", cmd)
	return 2
}

// flags is one command's flags, with the two every command shares.
type flags struct {
	*flag.FlagSet
	w where
}

func (a *app) flags(name, synopsis string) *flags {
	f := &flags{FlagSet: flag.NewFlagSet(name, flag.ContinueOnError)}
	f.SetOutput(a.err)
	f.Usage = func() {
		fmt.Fprintf(a.err, "Usage: %s\n", synopsis)
		f.PrintDefaults()
	}
	f.StringVar(&f.w.dir, "dir", a.env("HOLDFAST_DIR"), "the project `folder` (default: the nearest one with a .holdfast folder)")
	f.BoolVar(&f.w.global, "global", false, "use the store for the whole machine, in your home folder")
	return f
}

// parse reads flags wherever they stand among the arguments, and returns the
// arguments that are not flags. ok is false when the command should stop.
func (f *flags) parse(args []string) (rest []string, code int, ok bool) {
	for {
		if err := f.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, 0, false
			}
			return nil, 2, false
		}
		if f.NArg() == 0 {
			return rest, 0, true
		}
		rest = append(rest, f.Arg(0))
		args = f.Args()[1:]
	}
}

// fail reports an error and picks the exit code.
func (a *app) fail(err error) int {
	var refused *engine.Refused
	var rejected *store.Rejected
	fmt.Fprintln(a.err, "holdfast:", err)
	if errors.As(err, &refused) || errors.As(err, &rejected) {
		return 1
	}
	return 2
}

func (a *app) misuse(f *flags, why string) int {
	fmt.Fprintln(a.err, "holdfast:", why)
	f.Usage()
	return 2
}

// who names the person or agent a change is made by.
func (a *app) who(as string) string {
	for _, s := range []string{as, a.env("HOLDFAST_AGENT"), a.env("USER"), a.env("LOGNAME")} {
		if s != "" {
			return s
		}
	}
	return ""
}

// engine opens the store and the machine around it.
func (a *app) engine(w where) (*engine.Engine, *store.Dir, error) {
	d, err := a.open(w)
	if err != nil {
		return nil, nil, err
	}
	return &engine.Engine{Store: d, World: a.world(d.Root())}, d, nil
}

func (a *app) world(root string) *world.OS {
	return &world.OS{Root: root, UserAgent: "holdfast/" + version}
}

// done closes a store and mentions journal lines that could not be read.
func (a *app) done(d *store.Dir) {
	switch n := d.Damaged(); {
	case n == 1:
		fmt.Fprintln(a.err, "holdfast: 1 damaged journal line was skipped; the rest of the memory is intact")
	case n > 1:
		fmt.Fprintf(a.err, "holdfast: %d damaged journal lines were skipped; the rest of the memory is intact\n", n)
	}
	d.Close()
}

func (a *app) json(v any) int {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return a.fail(err)
	}
	return 0
}

// interrupted is what a command runs under: it ends when the person stops
// the command. It is a variable so that a test can stand in for the person.
var interrupted = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func (a *app) cmdInit(args []string) int {
	f := a.flags("init", "holdfast init [--global]")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return a.misuse(f, "init takes no arguments; use --dir to name another folder")
	}
	var path string
	switch {
	case f.w.global || f.w.dir != "":
		var err error
		if path, _, err = a.path(f.w); err != nil {
			return a.fail(err)
		}
	default:
		// Here, not in a folder above: a project inside another project
		// gets a memory of its own.
		path = filepath.Join(a.cwd, store.DirName)
	}
	_, statErr := os.Stat(filepath.Join(path, store.PolicyFile))
	d, err := store.Init(path, basePolicy(path))
	if err != nil {
		return a.fail(err)
	}
	defer d.Close()
	if statErr == nil {
		fmt.Fprintf(a.out, "Already set up: %s\n", d.Path())
		return 0
	}
	whole := filepath.Dir(d.Path()) == home()
	fmt.Fprintf(a.out, "Created %s\n\n", d.Path())
	if whole {
		fmt.Fprintln(a.out, "  facts.jsonl      the facts about this machine, for every project on it.")
		fmt.Fprintln(a.out, "  evidence.jsonl   the proof gathered each time they are checked.")
	} else {
		fmt.Fprintln(a.out, "  facts.jsonl      the facts. Commit it, and everyone on the project shares them.")
		fmt.Fprintln(a.out, "  evidence.jsonl   proof gathered on this machine. It stays out of git.")
	}
	if whole {
		fmt.Fprintln(a.out, "  policy           what checks may touch: services on this machine, and no")
		fmt.Fprintln(a.out, "                   files until you name a folder (holdfast allow files ~/notes).")
	} else {
		fmt.Fprintln(a.out, "  policy           what checks may touch: this folder and this machine, until")
		fmt.Fprintln(a.out, "                   you allow more (holdfast allow ...).")
	}
	g := ""
	if f.w.global {
		g = " --global"
	}
	fmt.Fprintln(a.out, "\nNext")
	fmt.Fprintf(a.out, "  holdfast add%s \"The API listens on port 8080\" --check \"port localhost:8080\"\n", g)
	fmt.Fprintf(a.out, "  claude mcp add holdfast -- holdfast mcp%s      give Claude Code this memory\n", g)
	fmt.Fprintf(a.out, "  holdfast serve%s                               read the ledger in a browser\n", g)
	return 0
}

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(s string) error { *l = append(*l, s); return nil }

func (a *app) cmdAdd(args []string) int {
	f := a.flags("add", `holdfast add "<claim>" [--check "<check>"] [--tag <tag>]... [--ttl 30d] [--replaces <id>]`)
	var tags list
	check := f.String("check", "", "what proves the claim, in the check language (see: holdfast help)")
	f.Var(&tags, "tag", "a `tag` to find the fact by; may be repeated")
	ttl := f.String("ttl", "", "for a fact without a check: how `long` to believe it (default 30d)")
	replaces := f.String("replaces", "", "the `id` of a fact this one takes the place of")
	as := f.String("as", "", "the `name` to store the fact under (default: you)")
	asJSON := f.Bool("json", false, "print the stored fact as JSON")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(rest) != 1 {
		return a.misuse(f, "give the claim as one quoted sentence")
	}
	e, d, err := a.engine(f.w)
	if err != nil {
		return a.fail(err)
	}
	defer a.done(d)
	ctx, stop := interrupted()
	defer stop()
	v, already, err := e.Remember(ctx, engine.Claim{
		Claim: rest[0], Check: *check, Tags: tags, Replaces: *replaces, TTL: *ttl, By: a.who(*as),
	})
	if err != nil {
		var refused *engine.Refused
		if errors.As(err, &refused) {
			fmt.Fprintln(a.err, "Not stored:", refused.Why)
			return 1
		}
		return a.fail(err)
	}
	if *asJSON {
		return a.json(map[string]any{"fact": v, "already": already})
	}
	head := "Stored."
	switch {
	case already:
		head = "Already remembered."
	case v.Status == fact.Holds:
		head = "Stored, and proved just now."
	case v.Status == fact.Blocked:
		head = "Stored, but not proved: its check was not run."
	case v.Check == "":
		head = "Stored as a note. It has no check, so it will go stale."
	}
	fmt.Fprintln(a.out, head)
	fmt.Fprintln(a.out, a.paint(v.Text(e.World.Now())))
	return 0
}

func (a *app) cmdList(args []string) int {
	f := a.flags("list", "holdfast list [--status <status>] [--tag <tag>] [--all] [--json]")
	status := f.String("status", "", "only facts that are `holds`, broken, blocked, unproven or stale")
	tag := f.String("tag", "", "only facts with this `tag`")
	all := f.Bool("all", false, "include retracted and replaced facts")
	asJSON := f.Bool("json", false, "print JSON")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return a.misuse(f, "list takes no arguments; to search, use: holdfast recall \"<question>\"")
	}
	// A status nobody has would match nothing, and "0 facts" reads as good news.
	switch fact.Status(strings.ToLower(*status)) {
	case "", fact.Holds, fact.Broken, fact.Blocked, fact.Unproven, fact.Stale:
	default:
		return a.misuse(f, fmt.Sprintf("%q is not a status; write holds, broken, blocked, unproven or stale", *status))
	}
	e, d, err := a.engine(f.w)
	if err != nil {
		return a.fail(err)
	}
	defer a.done(d)
	l, err := e.List(*all)
	if err != nil {
		return a.fail(err)
	}
	now := e.World.Now()
	if *status == "" && *tag == "" {
		if *asJSON {
			return a.json(l)
		}
		fmt.Fprintln(a.out, a.paint(l.Text(now)))
		return 0
	}
	keep := l.Facts[:0:0]
	for _, v := range l.Facts {
		if *status != "" && string(v.Status) != strings.ToLower(*status) {
			continue
		}
		if *tag != "" && !has(v.Tags, strings.ToLower(strings.TrimPrefix(*tag, "#"))) {
			continue
		}
		keep = append(keep, v)
	}
	if *asJSON {
		return a.json(map[string]any{"facts": keep, "counts": l.Counts})
	}
	fmt.Fprintf(a.out, "%d of %d facts.\n", len(keep), l.Counts.Total)
	if len(keep) > 0 {
		fmt.Fprintln(a.out)
		fmt.Fprintln(a.out, a.paint(blocks(keep, now)))
	}
	return 0
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func blocks(views []engine.View, now time.Time) string {
	parts := make([]string, len(views))
	for i, v := range views {
		parts[i] = v.Text(now)
	}
	return strings.Join(parts, "\n")
}

func (a *app) cmdCheck(args []string) int {
	f := a.flags("check", "holdfast check [<id>...] [--strict] [--quiet] [--json]")
	strict := f.Bool("strict", false, "also exit 1 when a fact is blocked")
	quiet := f.Bool("quiet", false, "print only what does not hold")
	asJSON := f.Bool("json", false, "print JSON")
	ids, code, ok := f.parse(args)
	if !ok {
		return code
	}
	e, d, err := a.engine(f.w)
	if err != nil {
		return a.fail(err)
	}
	defer a.done(d)
	ctx, stop := interrupted()
	defer stop()
	v, err := e.Verify(ctx, ids)
	if err != nil {
		return a.fail(err)
	}
	now := e.World.Now()
	switch {
	case *asJSON:
		if c := a.json(v); c != 0 {
			return c
		}
	case *quiet:
		var bad []engine.View
		for _, x := range v.Facts {
			if x.Status != fact.Holds || x.Late {
				bad = append(bad, x)
			}
		}
		head := v.Counts.String() + "."
		if v.Counts.Late > v.Counts.TimedOut {
			head += fmt.Sprintf(" %d could not be checked again in time.", v.Counts.Late)
		}
		fmt.Fprintln(a.out, head)
		if len(bad) > 0 {
			fmt.Fprintln(a.out)
			fmt.Fprintln(a.out, a.paint(blocks(bad, now)))
		}
		if v.Note != "" {
			fmt.Fprintln(a.out, "\n"+v.Note)
		}
	default:
		fmt.Fprintln(a.out, a.paint(v.Text(now)))
	}
	// A check that did not finish proved nothing, so a script that asks
	// "does everything still hold?" must not be told yes.
	if v.Counts.Broken > 0 || v.Counts.Late > 0 || (*strict && v.Counts.Blocked > 0) {
		return 1
	}
	return 0
}

func (a *app) cmdRecall(args []string) int {
	f := a.flags("recall", `holdfast recall "<question>" [-n <count>] [--json]`)
	n := f.Int("n", 0, "at most this many facts (default 8)")
	asJSON := f.Bool("json", false, "print JSON")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	e, d, err := a.engine(f.w)
	if err != nil {
		return a.fail(err)
	}
	defer a.done(d)
	ctx, stop := interrupted()
	defer stop()
	r, err := e.Recall(ctx, strings.Join(rest, " "), *n)
	if err != nil {
		return a.fail(err)
	}
	if *asJSON {
		return a.json(r)
	}
	fmt.Fprintln(a.out, a.paint(r.Text(e.World.Now())))
	return 0
}

func (a *app) cmdWhy(args []string) int {
	f := a.flags("why", "holdfast why <id> [--json]")
	asJSON := f.Bool("json", false, "print JSON")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(rest) != 1 {
		return a.misuse(f, "name one fact by its id")
	}
	e, d, err := a.engine(f.w)
	if err != nil {
		return a.fail(err)
	}
	defer a.done(d)
	v, err := e.Get(rest[0])
	if err != nil {
		return a.fail(err)
	}
	if *asJSON {
		return a.json(v)
	}
	fmt.Fprintln(a.out, a.paint(detail(v, e.World.Now())))
	return 0
}

// detail is one fact in full: what View.Text says, then where it came from
// and every change of state it has been through.
func detail(v engine.View, now time.Time) string {
	var b strings.Builder
	text := v.Text(now)
	b.WriteString(text)
	pad := strings.Repeat(" ", 10)
	if v.Check != "" && !strings.Contains(text, "\n"+pad+"check: ") {
		fmt.Fprintf(&b, "\n%scheck: %s", pad, v.Check)
	}
	from := "stored " + fact.Ago(now, v.Created)
	if v.By != "" {
		from += " by " + v.By
	}
	if len(v.Tags) > 0 {
		from += " · tags: " + strings.Join(v.Tags, ", ")
	}
	fmt.Fprintf(&b, "\n%s%s", pad, from)
	if v.VouchedBy != "" && v.NotedAt != nil {
		fmt.Fprintf(&b, "\n%svouched for by %s, %s", pad, v.VouchedBy, fact.Ago(now, *v.NotedAt))
	}
	if v.Replaces != "" {
		fmt.Fprintf(&b, "\n%sreplaces %s", pad, v.Replaces)
	}
	if len(v.History) > 0 {
		b.WriteString("\n\nHistory\n")
		for _, h := range v.History {
			fmt.Fprintf(&b, "  %-9s %-8s %s\n", fact.Ago(now, h.At), h.Status, h.Detail)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (a *app) cmdVouch(args []string) int {
	f := a.flags("vouch", "holdfast vouch <id>")
	as := f.String("as", "", "the `name` to vouch under (default: you)")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(rest) != 1 {
		return a.misuse(f, "name one fact by its id")
	}
	e, d, err := a.engine(f.w)
	if err != nil {
		return a.fail(err)
	}
	defer a.done(d)
	v, err := e.Vouch(rest[0], a.who(*as))
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintln(a.out, "Vouched for. Its time to live starts again from now.")
	fmt.Fprintln(a.out, a.paint(v.Text(e.World.Now())))
	return 0
}

func (a *app) cmdRetract(args []string) int {
	f := a.flags("retract", `holdfast retract <id> --reason "<why>"`)
	reason := f.String("reason", "", "why the fact is withdrawn; kept with it")
	as := f.String("as", "", "the `name` to retract under (default: you)")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(rest) != 1 {
		return a.misuse(f, "name one fact by its id")
	}
	e, d, err := a.engine(f.w)
	if err != nil {
		return a.fail(err)
	}
	defer a.done(d)
	v, err := e.Retract(rest[0], *reason, a.who(*as))
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintln(a.out, a.paint(v.Text(e.World.Now())))
	return 0
}

func (a *app) cmdBrief(args []string) int {
	f := a.flags("brief", "holdfast brief")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return a.misuse(f, "brief takes no arguments")
	}
	e, d, err := a.engine(f.w)
	var none noStore
	if errors.As(err, &none) {
		// A session-start hook runs in every folder; most have no store.
		return 0
	}
	if err != nil {
		return a.fail(err)
	}
	defer a.done(d)
	ctx, stop := interrupted()
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	proved, err := e.Verify(ctx, nil)
	if err != nil {
		return a.fail(err)
	}
	l, err := e.List(false)
	if err != nil {
		return a.fail(err)
	}
	if l.Counts.Total == 0 {
		return 0
	}
	now := e.World.Now()
	// The list is as last known. A check that did not finish just now is
	// marked on it first, so the summary does not count that fact as holding.
	late := l.Mark(proved.Facts)
	whose := "this project's memory"
	if d.Root() == home() {
		whose = "this machine's memory"
	}
	fmt.Fprintf(a.out, "Holdfast, %s: %s.", whose, l.Counts)
	switch {
	case len(proved.Facts) == 0:
	case late == 0:
		fmt.Fprint(a.out, " Every check was run just now.")
	case late == 1:
		fmt.Fprint(a.out, " Not everything could be proved again: 1 check did not finish in time.")
	default:
		fmt.Fprintf(a.out, " Not everything could be proved again: %d checks did not finish in time.", late)
	}
	fmt.Fprintln(a.out)
	// A fact whose check did not finish is not proved, so it is shown in full
	// with the ones that need attention, never under "Proved and holding".
	var bad, good []engine.View
	for _, v := range l.Facts {
		if v.Status == fact.Holds && !v.Late {
			good = append(good, v)
		} else {
			bad = append(bad, v)
		}
	}
	if len(bad) > 0 {
		fmt.Fprintln(a.out)
		fmt.Fprintln(a.out, a.paint(blocks(bad, now)))
	}
	if len(good) > 0 {
		fmt.Fprintln(a.out, "\nProved and holding:")
		for _, v := range good {
			fmt.Fprintf(a.out, "  %s  %s\n", v.ID, v.Claim)
		}
	}
	if l.Note != "" {
		fmt.Fprintln(a.out, "\n"+l.Note)
	}
	fmt.Fprintln(a.out, "\nBefore relying on any of this later in the session, ask again with the recall tool: it proves each fact again first.")
	return 0
}

func (a *app) cmdExport(args []string) int {
	f := a.flags("export", "holdfast export [--check]")
	f.Bool("md", true, "write Markdown (the only format)")
	check := f.Bool("check", false, "prove every fact again first")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return a.misuse(f, "export takes no arguments")
	}
	e, d, err := a.engine(f.w)
	if err != nil {
		return a.fail(err)
	}
	defer a.done(d)
	var proved engine.Verified
	if *check {
		ctx, stop := interrupted()
		defer stop()
		if proved, err = e.Verify(ctx, nil); err != nil {
			return a.fail(err)
		}
	}
	l, err := e.List(false)
	if err != nil {
		return a.fail(err)
	}
	// A check that did not finish just now proves nothing, so that fact is
	// written under "Timed out", never under "Holds".
	l.Mark(proved.Facts)
	fmt.Fprint(a.out, l.Markdown(e.World.Now()))
	return 0
}

func (a *app) cmdPolicy(args []string) int {
	f := a.flags("policy", "holdfast policy")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return a.misuse(f, "policy takes no arguments; to widen it, use: holdfast allow")
	}
	d, err := a.open(f.w)
	if err != nil {
		return a.fail(err)
	}
	defer d.Close()
	text, approved, err := d.PolicyText()
	if err != nil {
		return a.fail(err)
	}
	// The file may be one nobody here has read yet, so nothing in it is let
	// through that could clear the screen or write over a line.
	fmt.Fprintf(a.out, "%s\n\n%s\n", filepath.Join(d.Path(), store.PolicyFile), strings.TrimRight(policy.Printable(text), "\n"))
	if !approved {
		_, note := d.Policy()
		fmt.Fprintf(a.out, "\nNot in force: %s\n", note)
		return 1
	}
	if d.Unwidened() {
		fmt.Fprintln(a.out, "\nIn force: it allows nothing beyond the defaults, so it needs no approval.")
		return 0
	}
	fmt.Fprintln(a.out, "\nIn force: approved on this machine.")
	return 0
}

func (a *app) cmdAllow(args []string) int {
	f := a.flags("allow", "holdfast allow net <who>... | cmd <program> [<argument>...] | files <folder>...")
	// Flags come first here: everything after the kind is the rule itself,
	// and a rule for a command may well contain dashes.
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rest := f.Args()
	if len(rest) < 2 {
		return a.misuse(f, "say what to allow")
	}
	kind, vals := rest[0], rest[1:]
	if kind == "net" || kind == "files" {
		// Every other command takes its flags anywhere, so a flag after the
		// kind is an easy slip, and it would be written into the policy as
		// a host or a folder.
		for _, v := range vals {
			if strings.HasPrefix(v, "-") {
				return a.misuse(f, fmt.Sprintf("%q would become part of the rule; flags go before %q", v, kind))
			}
		}
	}
	var line string
	switch kind {
	case "net":
		for _, v := range vals {
			if _, err := policy.ParseNetRule(v); err != nil {
				return a.fail(err)
			}
		}
		line = "net " + strings.Join(vals, " ")
	case "cmd", "files":
		// An empty name is a slip (a shell variable that was never set), not
		// a decision: as a folder it would stand for everything the store
		// is about, the whole home folder in the store for this machine.
		if kind == "cmd" && vals[0] == "" {
			return a.fail(errors.New("an empty cmd rule: the program has no name"))
		}
		q := make([]string, len(vals))
		for i, v := range vals {
			if kind == "files" && v == "" {
				return a.fail(errors.New("an empty files rule: to allow the store's whole folder, write: files ."))
			}
			q[i] = fact.Quote(v)
		}
		line = kind + " " + strings.Join(q, " ")
	default:
		return a.misuse(f, fmt.Sprintf("%q is not something a policy allows; write net, cmd or files", kind))
	}
	e, d, err := a.engine(f.w)
	if err != nil {
		return a.fail(err)
	}
	defer a.done(d)
	text, approved, err := d.PolicyText()
	if err != nil {
		return a.fail(err)
	}
	if !approved {
		fmt.Fprintln(a.err, "holdfast: the policy file has changed since it was last approved on this machine, so it is not widened blind.")
		fmt.Fprintln(a.err, "Read it with \"holdfast policy\", approve it with \"holdfast trust\", then allow more.")
		return 1
	}
	// The last line of a file a person wrote may not end in a newline.
	if strings.Contains("\n"+text+"\n", "\n"+line+"\n") {
		fmt.Fprintln(a.out, "Already allowed:", line)
		return 0
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if err := d.SetPolicyText(text + line + "\n"); err != nil {
		return a.fail(err)
	}
	fmt.Fprintln(a.out, "Allowed:", line)

	// Anything that was waiting on this decision gets its answer now.
	l, err := e.List(false)
	if err != nil {
		return a.fail(err)
	}
	var waiting []string
	for _, v := range l.Facts {
		if v.Status == fact.Blocked {
			waiting = append(waiting, v.ID)
		}
	}
	if len(waiting) == 0 {
		return 0
	}
	ctx, stop := interrupted()
	defer stop()
	v, err := e.Verify(ctx, waiting)
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintln(a.out, "\nThe facts that were blocked, checked again:")
	fmt.Fprintln(a.out, a.paint(blocks(v.Facts, e.World.Now())))
	return 0
}

func (a *app) cmdTrust(args []string) int {
	f := a.flags("trust", "holdfast trust [--yes]")
	yes := f.Bool("yes", false, "approve without being asked")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return a.misuse(f, "trust takes no arguments")
	}
	d, err := a.open(f.w)
	if err != nil {
		return a.fail(err)
	}
	defer d.Close()
	text, approved, err := d.PolicyText()
	if err != nil {
		return a.fail(err)
	}
	p, err := policy.Parse(text)
	if err != nil {
		return a.fail(fmt.Errorf("the policy file has a mistake, so there is nothing to approve: %w", err))
	}
	fmt.Fprintf(a.out, "%s allows checks to:\n\n", filepath.Join(d.Path(), store.PolicyFile))
	rules := 0
	for _, x := range p.Files {
		fmt.Fprintf(a.out, "  read files in     %s\n", x)
		rules++
	}
	for _, x := range p.Net {
		fmt.Fprintf(a.out, "  connect to        %s\n", x)
		rules++
	}
	for _, x := range p.Cmds {
		fmt.Fprintf(a.out, "  run the command   %s\n", strings.Join(x, " "))
		rules++
	}
	if rules == 0 {
		fmt.Fprintln(a.out, "  nothing at all")
	}
	fmt.Fprintln(a.out)
	if approved && d.Unwidened() {
		fmt.Fprintln(a.out, "It allows nothing beyond the defaults, so there is nothing to approve.")
		return 0
	}
	if approved {
		fmt.Fprintln(a.out, "It is already approved on this machine.")
		return 0
	}
	if !*yes {
		if !a.prompt {
			fmt.Fprintln(a.err, "holdfast: not approved. Read the list above, then run this in a terminal, or pass --yes.")
			return 1
		}
		fmt.Fprint(a.out, "Approve this on this machine? [y/N] ")
		answer, _ := bufio.NewReader(a.in).ReadString('\n')
		if s := strings.ToLower(strings.TrimSpace(answer)); s != "y" && s != "yes" {
			fmt.Fprintln(a.out, "Not approved. Only the defaults apply.")
			return 1
		}
	}
	if err := d.Trust(); err != nil {
		return a.fail(err)
	}
	fmt.Fprintln(a.out, "Approved on this machine. A later change to the file will need approving again.")
	return 0
}

func (a *app) cmdCompact(args []string) int {
	f := a.flags("compact", "holdfast compact")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return a.misuse(f, "compact takes no arguments")
	}
	d, err := a.open(f.w)
	if err != nil {
		return a.fail(err)
	}
	defer a.done(d)
	before, after, err := d.Compact()
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.out, "%s: %s, was %s. The facts themselves are never rewritten.\n", store.EvidenceFile, size(after), size(before))
	return 0
}

func size(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d bytes", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.1f kB", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/1000/1000)
	}
}

func (a *app) cmdMCP(args []string) int {
	f := a.flags("mcp", "holdfast mcp [--as <name>] [--no-init]")
	as := f.String("as", a.env("HOLDFAST_AGENT"), "the `name` facts are stored under (default: what the client calls itself)")
	noInit := f.Bool("no-init", false, "do not create a store when the first fact is remembered")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return a.misuse(f, "mcp takes no arguments")
	}
	path, found, err := a.path(f.w)
	if err != nil {
		return a.fail(err)
	}
	var st store.Store
	root := filepath.Dir(path)
	if found {
		d, err := store.Open(path, basePolicy(path))
		if err != nil {
			return a.fail(err)
		}
		defer d.Close()
		st, root = d, d.Root()
	} else {
		st = &lazy{path: path, make: !*noInit, init: f.w.init()}
	}
	ctx, stop := interrupted()
	defer stop()
	srv := &mcp.Server{
		Engine:  &engine.Engine{Store: st, World: a.world(root)},
		Version: version,
		Agent:   *as,
		Log:     a.err,
	}
	if err := srv.Serve(ctx, a.in, a.out); err != nil && !errors.Is(err, context.Canceled) {
		return a.fail(err)
	}
	return 0
}

func (a *app) cmdServe(args []string) int {
	f := a.flags("serve", "holdfast serve [--addr 127.0.0.1:7878] [--demo] [--expose]")
	addr := f.String("addr", "127.0.0.1:7878", "the `address` to listen on")
	isDemo := f.Bool("demo", false, "show a pretend project instead of a real store")
	expose := f.Bool("expose", false, "allow an address other machines can reach (anyone who can reach it can retract facts)")
	rest, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return a.misuse(f, "serve takes no arguments")
	}
	if !serve.Loopback(*addr) && !*expose {
		fmt.Fprintf(a.err, "holdfast: %s can be reached from other machines, and the ledger has no login. Use a 127.0.0.1 address, or pass --expose if that is what you want.\n", *addr)
		return 2
	}
	var door *api.API
	if *isDemo {
		door = demo.New(time.Now).API(version)
	} else {
		e, d, err := a.engine(f.w)
		if err != nil {
			return a.fail(err)
		}
		defer a.done(d)
		project := filepath.Base(d.Root())
		if d.Root() == home() {
			project = "this machine"
		}
		door = &api.API{Engine: e, Version: version, Who: a.who(""), Where: func() api.Where {
			text, approved, _ := d.PolicyText()
			return api.Where{Project: project, Policy: text, Approved: approved}
		}}
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return a.fail(fmt.Errorf("cannot listen on %s: %w", *addr, err))
	}
	srv := serve.Server(*addr, serve.Handler(door, web.Files(), *expose))
	ctx, stop := interrupted()
	defer stop()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		<-ctx.Done()
		quit, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(quit)
	}()
	fmt.Fprintf(a.out, "The ledger is open at http://%s  (Ctrl+C to close it)\n", ln.Addr())
	if !web.Built() {
		fmt.Fprintln(a.err, "holdfast: this binary was built without the web interface; only the API answers. Build with \"make build\".")
	}
	err = srv.Serve(ln)
	// Serve returns the moment Shutdown begins, while requests are still
	// being answered. They are given until Shutdown returns: leaving now
	// would close the store, and end the program, under them.
	stop()
	<-closed
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return a.fail(err)
	}
	return 0
}

// paint colours the label that starts a line, on a terminal.
func (a *app) paint(s string) string {
	if !a.color {
		return s
	}
	colours := map[string]string{
		"HOLDS": "32", "BROKEN": "1;31", "BLOCKED": "35", "STALE": "33",
		"UNPROVEN": "2", "RETRACTED": "2", "REPLACED": "2", "TIMED OUT": "33",
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		word, rest, ok := strings.Cut(line, " ")
		if tail, two := strings.CutPrefix(line, "TIMED OUT "); two {
			word, rest = "TIMED OUT", tail // the one label that is two words
		}
		if c, known := colours[word]; ok && known {
			lines[i] = "\x1b[" + c + "m" + word + "\x1b[0m " + rest
		} else if strings.HasPrefix(strings.TrimLeft(line, " "), "check: ") {
			lines[i] = "\x1b[2m" + line + "\x1b[0m"
		}
	}
	return strings.Join(lines, "\n")
}

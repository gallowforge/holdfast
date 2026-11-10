package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gallowforge/holdfast/internal/engine"
	"github.com/gallowforge/holdfast/internal/store"
)

// desk is where these tests run the command: a project folder, a home folder
// and the folders this machine keeps its approvals in, every one of them
// temporary, so nothing reads or writes the real ones.
type desk struct {
	t     *testing.T
	cwd   string
	home  string
	state string
	conf  string
	env   map[string]string
	// in is what the command reads; prompt says a person is at the keyboard.
	in     string
	prompt bool
	color  bool
}

// said is what a command printed and how it ended.
type said struct {
	code     int
	out, err string
}

const goMod = "module example.com/atlas\n\ngo 1.24\n"

// folder is a new empty folder, named the way the system itself names it.
func folder(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// sit is a desk in a folder with no store.
func sit(t *testing.T) *desk {
	t.Helper()
	d := &desk{t: t, cwd: folder(t), home: folder(t), state: folder(t), conf: folder(t), env: map[string]string{"USER": "alex"}}
	t.Setenv("HOME", d.home)
	t.Setenv("HOLDFAST_STATE", d.state)
	t.Setenv("XDG_CONFIG_HOME", d.conf)
	if p, found := store.Find(d.cwd); found {
		t.Skipf("there is a Holdfast store at %s, above the folders tests are given, so no folder here is without one", p)
	}
	return d
}

// project is a desk in a project that has a store and a go.mod to prove
// things about.
func project(t *testing.T) *desk {
	t.Helper()
	d := sit(t)
	d.write("go.mod", goMod)
	d.ok("init")
	return d
}

func (d *desk) run(args ...string) said {
	d.t.Helper()
	var out, errs bytes.Buffer
	a := &app{
		in: strings.NewReader(d.in), out: &out, err: &errs,
		cwd: d.cwd, env: func(name string) string { return d.env[name] },
		color: d.color, prompt: d.prompt,
	}
	code := a.run(args)
	return said{code, out.String(), errs.String()}
}

// ok runs a command that must succeed without a word of complaint.
func (d *desk) ok(args ...string) said {
	d.t.Helper()
	got := d.run(args...)
	if got.code != 0 || got.err != "" {
		d.t.Fatalf("holdfast %s: exit %d\n%s%s", strings.Join(args, " "), got.code, got.out, got.err)
	}
	return got
}

// write puts a file in the project.
func (d *desk) write(name, text string) {
	d.t.Helper()
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(d.cwd, name)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		d.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		d.t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// add stores a fact and returns it as the command printed it with --json.
func (d *desk) add(claim, check string, more ...string) engine.View {
	d.t.Helper()
	args := []string{"add", claim, "--json"}
	if check != "" {
		args = append(args, "--check", check)
	}
	got := d.ok(append(args, more...)...)
	var stored struct {
		Fact    engine.View `json:"fact"`
		Already bool        `json:"already"`
	}
	decode(d.t, got.out, &stored)
	if stored.Fact.ID == "" {
		d.t.Fatalf("add printed no fact: %s", got.out)
	}
	return stored.Fact
}

// listed is everything in the store, withdrawn facts too.
func (d *desk) listed(more ...string) engine.Listed {
	d.t.Helper()
	var l engine.Listed
	decode(d.t, d.ok(append([]string{"list", "--all", "--json"}, more...)...).out, &l)
	return l
}

// why is one fact as the store has it.
func (d *desk) why(id string, more ...string) engine.View {
	d.t.Helper()
	var v engine.View
	decode(d.t, d.ok(append([]string{"why", id, "--json"}, more...)...).out, &v)
	return v
}

// decode reads the one JSON value a command printed.
func decode(t *testing.T, text string, into any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(text))
	if err := dec.Decode(into); err != nil {
		t.Fatalf("the output is not JSON: %v\n%s", err, text)
	}
	if dec.More() {
		t.Fatalf("there is something after the JSON value:\n%s", text)
	}
}

// tree lists everything under a folder, by name.
func tree(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != dir && !e.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}

// says reports every piece of text that is missing.
func says(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("%s does not say %q:\n%s", what, want, text)
		}
	}
}

func lacks(t *testing.T, what, text string, bans ...string) {
	t.Helper()
	for _, ban := range bans {
		if strings.Contains(text, ban) {
			t.Errorf("%s says %q:\n%s", what, ban, text)
		}
	}
}

// everyCommand is each command with arguments that would be fine in a
// project with a store.
var everyCommand = [][]string{
	{"list"},
	{"add", "The API listens on port 8080"},
	{"check"},
	{"recall", "port"},
	{"why", "f-abc234"},
	{"vouch", "f-abc234"},
	{"retract", "f-abc234", "--reason", "it moved"},
	{"export"},
	{"policy"},
	{"allow", "net", "staging.example.com"},
	{"trust"},
	{"compact"},
	{"serve", "--addr", "127.0.0.1:0"},
}

// --- the command itself ---

func TestNoArgumentsAndHelpPrintTheWholeUsage(t *testing.T) {
	d := sit(t)
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}} {
		got := d.run(args...)
		if got.code != 0 || got.err != "" {
			t.Errorf("holdfast %v: exit %d, complaint %q", args, got.code, got.err)
		}
		says(t, "the usage", got.out,
			"Usage: holdfast <command> [arguments]",
			"\n  init ", "\n  mcp ", "\n  serve ", "\n  add ", "\n  list ", "\n  check ", "\n  recall ", "\n  why ",
			"\n  vouch ", "\n  retract ", "\n  brief ", "\n  export ", "\n  policy ", "\n  allow ", "\n  trust ",
			"\n  compact ", "\n  version",
			// The language of checks, a line for each kind.
			"\n  file <path>", "\n  contains <path> <text>", "\n  port <host:port>", "\n  says <text> <program>",
			`Start any check with "not"`,
			"--dir <folder>", "HOLDFAST_DIR", "--global")
		lacks(t, "the usage", got.out, "%!", "%s")
	}
	if left := tree(t, d.cwd); len(left) != 0 {
		t.Errorf("asking for help left %v behind", left)
	}
}

func TestVersionPrintsTheVersionTheBuildSet(t *testing.T) {
	d := sit(t)
	old := version
	version = "1.2.3-test"
	t.Cleanup(func() { version = old })
	for _, arg := range []string{"version", "--version"} {
		if got := d.run(arg); got.code != 0 || got.out != "holdfast 1.2.3-test\n" || got.err != "" {
			t.Errorf("holdfast %s: exit %d, %q, %q", arg, got.code, got.out, got.err)
		}
	}
}

func TestAnUnknownCommandExitsTwoAndPointsAtHelp(t *testing.T) {
	d := project(t)
	for _, name := range []string{"frobnicate", "Init", "--list", "", "lst"} {
		got := d.run(name, "list")
		want := `holdfast: there is no command "` + name + `". Run "holdfast help" for the list.` + "\n"
		if got.code != 2 || got.out != "" || got.err != want {
			t.Errorf("holdfast %q: exit %d, %q, %q; want exit 2 and %q", name, got.code, got.out, got.err, want)
		}
	}
}

// Every command the usage names is one the command line knows, with a help
// of its own.
func TestEveryCommandExplainsItselfAndTouchesNothing(t *testing.T) {
	d := sit(t)
	names := map[string]string{
		"init": "init", "mcp": "mcp", "serve": "serve", "add": "add", "list": "list", "check": "check",
		"recall": "recall", "why": "why", "vouch": "vouch", "retract": "retract", "brief": "brief",
		"export": "export", "policy": "policy", "allow": "allow", "trust": "trust", "compact": "compact",
		// Other names for the same things.
		"remember": "add", "ls": "list", "verify": "check", "show": "why",
	}
	for name, known := range names {
		for _, flag := range []string{"-h", "--help"} {
			got := d.run(name, flag)
			if got.code != 0 || got.out != "" {
				t.Errorf("holdfast %s %s: exit %d, printed %q", name, flag, got.code, got.out)
			}
			if !strings.HasPrefix(got.err, "Usage: holdfast "+known) {
				t.Errorf("holdfast %s %s explains itself as: %q", name, flag, got.err)
			}
			says(t, "the help of "+name, got.err, "-dir folder", "-global")
		}
	}
	if left := tree(t, d.cwd); len(left) != 0 {
		t.Errorf("asking a command for its help left %v behind", left)
	}
}

func TestAFlagACommandDoesNotHaveExitsTwo(t *testing.T) {
	d := project(t)
	before := tree(t, d.cwd)
	for _, args := range [][]string{
		{"list", "--nope"},
		{"add", "A claim", "--nope"},
		{"check", "--nope"},
		{"recall", "port", "--limit", "3"},
		{"init", "--force"},
		{"brief", "--nope"},
		{"allow", "--nope", "net", "example.com"},
		{"serve", "--port", "1"},
		{"mcp", "--nope"},
		// A flag that wants a value and is given none.
		{"add", "A claim", "--check"},
		{"recall", "-n", "many"},
	} {
		got := d.run(args...)
		if got.code != 2 || got.out != "" {
			t.Errorf("holdfast %v: exit %d, printed %q; want exit 2", args, got.code, got.out)
		}
		says(t, "the complaint", got.err, "Usage: holdfast "+args[0])
	}
	if after := tree(t, d.cwd); strings.Join(after, " ") != strings.Join(before, " ") {
		t.Errorf("a command that was not understood changed the project: %v, was %v", after, before)
	}
	if n := d.listed().Counts.Total; n != 0 {
		t.Errorf("a command that was not understood stored %d facts", n)
	}
}

// --- where the store is ---

func TestWithoutAStoreEveryCommandSaysHowToCreateOne(t *testing.T) {
	d := sit(t)
	other := folder(t)
	for _, args := range everyCommand {
		got := d.run(args...)
		if got.code != 2 || got.out != "" {
			t.Errorf("holdfast %v: exit %d, printed %q; want exit 2", args, got.code, got.out)
		}
		says(t, "the complaint of "+args[0], got.err, "holdfast: there is no Holdfast store here", "Create one with: holdfast init\n")

		// The store for the whole machine is a different one to create.
		global := d.run(append(args, "--global")...)
		if args[0] == "allow" {
			// allow takes its flags first: what follows the kind is the rule.
			global = d.run(append([]string{"allow", "--global"}, args[1:]...)...)
		}
		if global.code != 2 || global.out != "" || !strings.HasSuffix(global.err, "Create one with: holdfast init --global\n") {
			t.Errorf("holdfast %v --global: exit %d, %q, %q", args, global.code, global.out, global.err)
		}

		// And so is a project named with --dir: plain "holdfast init" would
		// create a store in this folder, not in that one.
		named := d.run(append(args, "--dir", other)...)
		if args[0] == "allow" {
			named = d.run(append([]string{"allow", "--dir", other}, args[1:]...)...)
		}
		if named.code != 2 || named.out != "" || !strings.HasSuffix(named.err, "Create one with: holdfast init --dir "+other+"\n") {
			t.Errorf("holdfast %v --dir: exit %d, %q, %q", args, named.code, named.out, named.err)
		}
	}
	if left := append(append(tree(t, d.cwd), tree(t, d.home)...), tree(t, other)...); len(left) != 0 {
		t.Errorf("commands that found no store left %v behind", left)
	}
}

// A hook that runs at the start of every session calls brief in every
// folder; most have no store, and that is not worth a word.
func TestBriefIsSilentWhereThereIsNothingToSay(t *testing.T) {
	d := sit(t)
	if got := d.run("brief"); got.code != 0 || got.out != "" || got.err != "" {
		t.Errorf("brief with no store: exit %d, %q, %q; want silence", got.code, got.out, got.err)
	}
	if left := tree(t, d.cwd); len(left) != 0 {
		t.Errorf("brief created %v", left)
	}
	d.ok("init")
	if got := d.run("brief"); got.code != 0 || got.out != "" || got.err != "" {
		t.Errorf("brief with an empty store: exit %d, %q, %q; want silence", got.code, got.out, got.err)
	}
	if got := d.run("brief", "now"); got.code != 2 || !strings.Contains(got.err, "brief takes no arguments") {
		t.Errorf("brief with an argument: exit %d, %q", got.code, got.err)
	}
}

func TestInitCreatesAStoreHereAndSaysWhatIsInIt(t *testing.T) {
	d := sit(t)
	got := d.ok("init")
	path := filepath.Join(d.cwd, ".holdfast")
	says(t, "init", got.out, "Created "+path+"\n", "facts.jsonl", "evidence.jsonl", "policy",
		"this folder and this machine",
		`holdfast add "The API listens on port 8080" --check "port localhost:8080"`,
		"claude mcp add holdfast -- holdfast mcp ", "holdfast serve ")
	lacks(t, "init in a project", got.out, "--global")

	// Everything it says it created is there.
	want := []string{".holdfast/.gitattributes", ".holdfast/.gitignore", ".holdfast/evidence.jsonl", ".holdfast/facts.jsonl", ".holdfast/policy"}
	left := tree(t, d.cwd)
	for _, name := range want {
		if !has(left, name) {
			t.Errorf("init did not create %s; the project holds %v", name, left)
		}
	}
	// Proof stays out of git, and two people's facts merge without conflict.
	says(t, ".gitignore", read(t, filepath.Join(path, ".gitignore")), "evidence.jsonl\n")
	says(t, ".gitattributes", read(t, filepath.Join(path, ".gitattributes")), "facts.jsonl merge=union")

	// The policy it wrote is in force at once: this folder and this machine.
	policy := d.ok("policy")
	says(t, "the policy", policy.out, filepath.Join(path, "policy"), "\nfiles .\n", "\nnet loopback\n", "In force: approved on this machine.")

	// And the store works.
	if got := d.ok("list"); got.out != "Nothing is remembered here yet.\n" {
		t.Errorf("a new store lists as %q", got.out)
	}
}

func TestInitASecondTimeChangesNothing(t *testing.T) {
	d := project(t)
	kept := d.add("Go is pinned to 1.24", `contains go.mod "go 1.24"`)
	d.ok("allow", "net", "staging.example.com")
	path := filepath.Join(d.cwd, ".holdfast")
	before := read(t, filepath.Join(path, "policy")) + read(t, filepath.Join(path, "facts.jsonl"))

	got := d.ok("init")
	if got.out != "Already set up: "+path+"\n" {
		t.Errorf("a second init said %q", got.out)
	}
	if after := read(t, filepath.Join(path, "policy")) + read(t, filepath.Join(path, "facts.jsonl")); after != before {
		t.Error("a second init rewrote the policy or the facts")
	}
	if l := d.listed(); l.Counts.Total != 1 || l.Facts[0].ID != kept.ID {
		t.Errorf("after a second init the store holds %+v", l.Counts)
	}
	if got := d.run("init", "here"); got.code != 2 || !strings.Contains(got.err, "init takes no arguments") {
		t.Errorf("init with an argument: exit %d, %q", got.code, got.err)
	}
}

func TestTheNearestStoreAtOrAboveTheFolderIsUsed(t *testing.T) {
	d := project(t)
	kept := d.add("Go is pinned to 1.24", `contains go.mod "go 1.24"`)
	top := d.cwd

	// From a folder deep inside the project, it is still the project's store,
	// and checks still read from the project's root.
	d.cwd = filepath.Join(top, "internal", "api")
	d.write("handler.go", "package api\n")
	if l := d.listed(); l.Counts.Total != 1 || l.Facts[0].ID != kept.ID {
		t.Fatalf("from a folder inside the project the store holds %+v", l.Counts)
	}
	inner := d.add("The handler is written", "file internal/api/handler.go")
	if inner.Status != "holds" {
		t.Errorf("a check from inside the project is %s: %s; paths are relative to the project root", inner.Status, inner.Detail)
	}
	if got := d.run("check"); got.code != 0 {
		t.Errorf("check from inside the project: exit %d\n%s%s", got.code, got.out, got.err)
	}

	// A project inside a project gets a memory of its own, created where
	// init was run and not in the folder above.
	got := d.ok("init")
	says(t, "init", got.out, "Created "+filepath.Join(d.cwd, ".holdfast"))
	if l := d.listed(); l.Counts.Total != 0 {
		t.Errorf("the inner project's new store already holds %d facts", l.Counts.Total)
	}
	d.add("The inner project has a note", "")
	d.cwd = top
	if l := d.listed(); l.Counts.Total != 2 {
		t.Errorf("the outer project holds %d facts, want its own 2", l.Counts.Total)
	}
}

func TestDirNamesTheProjectOrTheStoreItself(t *testing.T) {
	d := sit(t)
	other := folder(t)
	d.write(filepath.Join(other, "go.mod"), goMod)
	got := d.ok("init", "--dir", other)
	says(t, "init --dir", got.out, "Created "+filepath.Join(other, ".holdfast"))
	if left := tree(t, d.cwd); len(left) != 0 {
		t.Errorf("init --dir also created %v in the current folder", left)
	}
	kept := d.add("Go is pinned to 1.24", `contains go.mod "go 1.24"`, "--dir", other)
	if kept.Status != "holds" {
		t.Errorf("the fact is %s: %s; with --dir, paths are relative to that project", kept.Status, kept.Detail)
	}

	for name, how := range map[string]func() []string{
		"the project folder":      func() []string { return []string{"--dir", other} },
		"the store folder itself": func() []string { return []string{"--dir", filepath.Join(other, ".holdfast")} },
		"with an equals sign":     func() []string { return []string{"--dir=" + other} },
		"HOLDFAST_DIR":            func() []string { d.env["HOLDFAST_DIR"] = other; return nil },
	} {
		t.Run(name, func(t *testing.T) {
			defer delete(d.env, "HOLDFAST_DIR")
			if l := d.listed(how()...); l.Counts.Total != 1 || l.Facts[0].ID != kept.ID {
				t.Errorf("the store holds %+v, want the one fact", l.Counts)
			}
		})
	}

	// The flag is the later word, so it wins over the environment.
	empty := folder(t)
	d.env["HOLDFAST_DIR"] = empty
	if l := d.listed("--dir", other); l.Counts.Total != 1 {
		t.Errorf("--dir did not win over HOLDFAST_DIR: %+v", l.Counts)
	}
	if got := d.run("list"); got.code != 2 || !strings.Contains(got.err, "there is no Holdfast store") {
		t.Errorf("list in a folder named by HOLDFAST_DIR that has no store: exit %d, %q", got.code, got.err)
	}
	delete(d.env, "HOLDFAST_DIR")

	// A folder given by a path from here.
	t.Chdir(d.cwd)
	d.ok("init", "--dir", "sub")
	if left := tree(t, d.cwd); !has(left, "sub/.holdfast/policy") {
		t.Errorf("init --dir sub created %v", left)
	}
}

func TestProjectCommandsLeaveTheHomeFolderAlone(t *testing.T) {
	d := project(t)
	other := folder(t)
	d.write(filepath.Join(other, "data.txt"), "hello\n")
	d.add("Go is pinned to 1.24", `contains go.mod "go 1.24"`)
	note := d.add("Deploys go out on Fridays", "")
	d.add("The data is there", "file "+filepath.Join(other, "data.txt"))
	for _, args := range [][]string{
		{"list"}, {"check"}, {"recall", "go"}, {"why", note.ID}, {"vouch", note.ID}, {"brief"}, {"export", "--check"},
		{"policy"}, {"allow", "files", other}, {"trust"}, {"compact"}, {"retract", note.ID, "--reason", "no longer so"},
	} {
		d.ok(args...)
	}
	if left := append(tree(t, d.home), tree(t, d.conf)...); len(left) != 0 {
		t.Errorf("a project's commands wrote %v in the home or the config folder", left)
	}
	// What this machine has approved is kept apart from every project, where
	// HOLDFAST_STATE says.
	if left := tree(t, d.state); !has(left, "approved") {
		t.Errorf("the approvals are not in the state folder, which holds %v", left)
	}
	if left := tree(t, other); len(left) != 1 {
		t.Errorf("the folder a check may read now holds %v", left)
	}
}

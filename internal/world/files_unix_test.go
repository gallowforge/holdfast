//go:build unix

package world

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/policy"
)

const secret = "TOP-SECRET-CONTENT"

// machine lays out a project folder with things outside it that checks must
// never reach:
//
//	parent/project/           the root (the policy's ".")
//	parent/project/a.txt
//	parent/project/sub/b.txt
//	parent/secret.txt         outside
//	parent/outside/inner.txt  outside
//	parent/home/notes.txt     the home folder (outside)
func machine(t *testing.T) (o *OS, parent string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "project")
	for name, data := range map[string]string{
		"project/a.txt":     "alpha\n",
		"project/sub/b.txt": "bravo\n",
		"secret.txt":        secret,
		"outside/inner.txt": secret,
		"home/notes.txt":    secret,
		"home/docs/d.txt":   "delta\n",
	} {
		p := filepath.Join(parent, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", filepath.Join(parent, "home"))
	return &OS{Root: root, UserAgent: "holdfast-test/1"}, parent
}

func link(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func blocked(err error) *policy.Blocked {
	var b *policy.Blocked
	if errors.As(err, &b) {
		return b
	}
	return nil
}

// readAll opens a path and returns its content, or the error from Open.
func readAll(t *testing.T, o *OS, roots []string, path string) (string, error) {
	t.Helper()
	f, err := o.Open(roots, path)
	if err != nil {
		if f != nil {
			t.Errorf("Open(%q) returned a file together with an error", path)
		}
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}
	return string(data), nil
}

// refused asserts that neither Stat nor Open will touch the path, and that
// the refusal is a policy answer that does not carry the secret.
func refused(t *testing.T, o *OS, roots []string, path string) {
	t.Helper()
	st, err := o.Stat(roots, path)
	if blocked(err) == nil {
		t.Errorf("Stat(%q, %q) = %+v, %v; want a *policy.Blocked", roots, path, st, err)
	}
	if st != (FileInfo{}) {
		t.Errorf("Stat(%q, %q) told something about a refused path: %+v", roots, path, st)
	}
	data, err := readAll(t, o, roots, path)
	if blocked(err) == nil {
		t.Errorf("Open(%q, %q) = %q, %v; want a *policy.Blocked", roots, path, data, err)
	}
	if data != "" || (err != nil && strings.Contains(err.Error(), secret)) {
		t.Errorf("Open(%q, %q) let content out: %q, %v", roots, path, data, err)
	}
}

func TestFilesInsideTheRoot(t *testing.T) {
	o, _ := machine(t)
	roots := []string{"."}

	st, err := o.Stat(roots, "a.txt")
	if err != nil || !st.Exists || !st.Regular || st.Dir || st.Size != 6 {
		t.Errorf("Stat(a.txt) = %+v, %v", st, err)
	}
	if age := time.Since(st.ModTime); age < 0 || age > time.Hour {
		t.Errorf("Stat(a.txt): modified %v ago", age)
	}
	st, err = o.Stat(roots, "sub")
	if err != nil || !st.Exists || !st.Dir || st.Regular {
		t.Errorf("Stat(sub) = %+v, %v", st, err)
	}
	for _, p := range []string{".", "./", ""} {
		if st, err := o.Stat(roots, p); err != nil || !st.Dir {
			t.Errorf("Stat(%q) = %+v, %v; want the root folder", p, st, err)
		}
	}
	for _, p := range []string{"missing", "sub/missing", "missing/deeper/still"} {
		if st, err := o.Stat(roots, p); err != nil || st != (FileInfo{}) {
			t.Errorf("Stat(%q) = %+v, %v; want simply not there", p, st, err)
		}
	}

	for path, want := range map[string]string{
		"a.txt":                            "alpha\n",
		"./a.txt":                          "alpha\n",
		"sub/b.txt":                        "bravo\n",
		"sub/../a.txt":                     "alpha\n", // leaves sub, not the root
		"sub/./b.txt":                      "bravo\n",
		filepath.Join(o.Root, "a.txt"):     "alpha\n", // absolute, and inside
		filepath.Join(o.Root, "sub/b.txt"): "bravo\n",
	} {
		if got, err := readAll(t, o, roots, path); err != nil || got != want {
			t.Errorf("Open(%q) = %q, %v; want %q", path, got, err, want)
		}
	}

	// A missing file is an ordinary error, not a policy refusal.
	if _, err := o.Open(roots, "missing"); !errors.Is(err, fs.ErrNotExist) || blocked(err) != nil {
		t.Errorf("Open(missing) = %v, want a not-exist error", err)
	}
}

func TestPathsOutsideTheRoot(t *testing.T) {
	o, parent := machine(t)
	for _, path := range []string{
		"../secret.txt",
		"..",
		"../",
		"../project/../secret.txt",
		"sub/../../secret.txt",
		"../../../../../../../../etc/passwd",
		filepath.Join(parent, "secret.txt"),
		filepath.Join(parent, "outside", "inner.txt"),
		filepath.Join(o.Root, "..", "secret.txt"),
		"/etc/passwd",
		"/",
		"~/notes.txt",
		"~",
		o.Root + "-other/a.txt", // shares the root's name as a prefix, and is not inside it
	} {
		refused(t, o, []string{"."}, path)
	}

	// The refusal says what a person would add, and nothing about the content.
	_, err := o.Stat([]string{"."}, "../secret.txt")
	b := blocked(err)
	if b == nil {
		t.Fatal("no refusal")
	}
	if b.Why != "../secret.txt is outside the folders the policy allows" || b.Fix != "holdfast allow files .." {
		t.Errorf("refusal = %q / %q", b.Why, b.Fix)
	}
	if want := "../secret.txt is outside the folders the policy allows. A person can allow it: holdfast allow files .."; err.Error() != want {
		t.Errorf("refusal text = %q, want %q", err, want)
	}
	if strings.Contains(err.Error(), parent) || strings.Contains(err.Error(), secret) {
		t.Errorf("the refusal says more than the check did: %q", err)
	}
}

func TestOnlyAllowedFolders(t *testing.T) {
	o, parent := machine(t)

	// A narrower policy: only sub.
	if got, err := readAll(t, o, []string{"sub"}, "sub/b.txt"); err != nil || got != "bravo\n" {
		t.Errorf("Open(sub/b.txt) under [sub] = %q, %v", got, err)
	}
	refused(t, o, []string{"sub"}, "a.txt")
	refused(t, o, []string{"sub"}, "sub/../a.txt")
	refused(t, o, []string{"sub"}, "subx")
	refused(t, o, []string{}, "a.txt")
	refused(t, o, nil, "a.txt")

	// Several folders: the first that holds the path is used.
	roots := []string{"sub", filepath.Join(parent, "outside"), "~/docs"}
	for path, want := range map[string]string{
		"sub/b.txt": "bravo\n",
		filepath.Join(parent, "outside/inner.txt"): secret,
		"~/docs/d.txt":         "delta\n",
		"../outside/inner.txt": secret, // relative spelling of an allowed folder
	} {
		if got, err := readAll(t, o, roots, path); err != nil || got != want {
			t.Errorf("Open(%q) = %q, %v; want %q", path, got, err, want)
		}
	}
	refused(t, o, roots, "~/notes.txt")
	refused(t, o, roots, "a.txt")
	refused(t, o, roots, filepath.Join(parent, "secret.txt"))

	// Home as a whole.
	if got, err := readAll(t, o, []string{"~"}, "~/notes.txt"); err != nil || got != secret {
		t.Errorf("Open(~/notes.txt) under [~] = %q, %v", got, err)
	}
	if st, err := o.Stat([]string{"~/"}, "~"); err != nil || !st.Dir {
		t.Errorf("Stat(~) under [~/] = %+v, %v", st, err)
	}
}

// A folder in the policy that does not exist, or is not a folder, allows nothing.
func TestMissingRootAllowsNothing(t *testing.T) {
	o, _ := machine(t)
	refused(t, o, []string{"nope"}, "nope/x.txt")
	refused(t, o, []string{"nope"}, "nope")
	refused(t, o, []string{"a.txt"}, "a.txt")
	refused(t, o, []string{"a.txt"}, "a.txt/x")
	// The next folder in the list is still tried.
	if got, err := readAll(t, o, []string{"nope", "."}, "a.txt"); err != nil || got != "alpha\n" {
		t.Errorf("Open(a.txt) under [nope .] = %q, %v", got, err)
	}
	// A project root that does not exist allows nothing either.
	gone := &OS{Root: filepath.Join(o.Root, "no-such-project")}
	refused(t, gone, []string{"."}, "a.txt")
}

func TestSymbolicLinks(t *testing.T) {
	o, parent := machine(t)
	in := func(name string) string { return filepath.Join(o.Root, name) }
	roots := []string{"."}

	// Links that stay inside work like the thing they point to.
	link(t, "a.txt", in("to-a"))
	link(t, "to-a", in("to-to-a"))
	link(t, "../a.txt", in("sub/up-to-a"))
	link(t, "sub", in("to-sub"))
	for path, want := range map[string]string{"to-a": "alpha\n", "to-to-a": "alpha\n", "sub/up-to-a": "alpha\n", "to-sub/b.txt": "bravo\n"} {
		st, err := o.Stat(roots, path)
		if err != nil || !st.Regular {
			t.Errorf("Stat(%q) = %+v, %v; want a regular file", path, st, err)
		}
		if got, err := readAll(t, o, roots, path); err != nil || got != want {
			t.Errorf("Open(%q) = %q, %v; want %q", path, got, err, want)
		}
	}
	if st, err := o.Stat(roots, "to-sub"); err != nil || !st.Dir {
		t.Errorf("Stat(to-sub) = %+v, %v; want a folder", st, err)
	}

	// Links that leave are refused, and nothing of the target comes back.
	link(t, "../secret.txt", in("rel-file"))
	link(t, filepath.Join(parent, "secret.txt"), in("abs-file"))
	link(t, "../outside", in("rel-dir"))
	link(t, filepath.Join(parent, "outside"), in("abs-dir"))
	link(t, "..", in("up"))
	link(t, "/", in("slash"))
	link(t, "rel-file", in("chain"))
	link(t, "../../secret.txt", in("sub/deep"))
	link(t, "/etc/passwd", in("passwd"))
	for _, path := range []string{
		"rel-file", "abs-file", "chain", "sub/deep", "passwd",
		"rel-dir/inner.txt", "abs-dir/inner.txt", "rel-dir", "abs-dir",
		"up/secret.txt", "up", "up/project/a.txt", "slash/etc/passwd", "slash",
	} {
		refused(t, o, roots, path)
	}
	_, err := o.Open(roots, "rel-file")
	if b := blocked(err); b == nil || b.Why != "rel-file leads outside the folders the policy allows (through a symbolic link)" || b.Fix != "" {
		t.Errorf("refusal for a link that leaves = %v", err)
	}

	// The folder a link leaves is the allowed one, not the project: with only
	// "sub" allowed, a link from sub up to a.txt is a way out.
	refused(t, o, []string{"sub"}, "sub/up-to-a")

	// A link to nowhere is simply not there.
	link(t, "no-such-file", in("dangling"))
	if st, err := o.Stat(roots, "dangling"); err != nil || st.Exists {
		t.Errorf("Stat(dangling) = %+v, %v; want not there", st, err)
	}
	// A link in a loop cannot be read, and does not hang.
	link(t, "loop-b", in("loop-a"))
	link(t, "loop-a", in("loop-b"))
	if st, err := o.Stat(roots, "loop-a"); err == nil && st.Exists {
		t.Errorf("Stat(loop-a) = %+v; want an error or not there", st)
	}
	if got, err := readAll(t, o, roots, "loop-a"); err == nil {
		t.Errorf("Open(loop-a) = %q; want an error", got)
	}
}

// An absolute link is refused even when it points back inside the folder.
// That is stricter than it has to be, and safe; this test pins it.
func TestAbsoluteLinkInside(t *testing.T) {
	o, _ := machine(t)
	link(t, filepath.Join(o.Root, "a.txt"), filepath.Join(o.Root, "abs-inside"))
	refused(t, o, []string{"."}, "abs-inside")
}

func TestOnlyRegularFilesAreOpened(t *testing.T) {
	o, _ := machine(t)
	roots := []string{"."}

	// A folder.
	for _, path := range []string{"sub", "."} {
		data, err := readAll(t, o, roots, path)
		if b := blocked(err); b == nil || !strings.HasSuffix(b.Why, " is not a regular file") {
			t.Errorf("Open(%q) = %q, %v; want a refusal: not a regular file", path, data, err)
		}
	}

	// A FIFO: opening one for reading waits for a writer, for ever.
	fifo := filepath.Join(o.Root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("cannot make a FIFO here: %v", err)
	}
	st, err := o.Stat(roots, "pipe")
	if err != nil || !st.Exists || st.Regular || st.Dir {
		t.Errorf("Stat(pipe) = %+v, %v; want something that exists and is not a regular file", st, err)
	}
	link(t, "pipe", filepath.Join(o.Root, "to-pipe"))
	for _, path := range []string{"pipe", "to-pipe"} {
		done := make(chan error, 1)
		go func() {
			f, err := o.Open(roots, path)
			if f != nil {
				f.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if b := blocked(err); b == nil || b.Why != path+" is not a regular file" {
				t.Errorf("Open(%q) = %v; want a refusal: not a regular file", path, err)
			}
		case <-time.After(10 * time.Second):
			// Let the stuck open go by giving the FIFO a writer, then fail.
			if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				w.Close()
			}
			t.Fatalf("Open(%q) is waiting on the FIFO", path)
		}
	}
}

func TestUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	o, _ := machine(t)
	if err := os.Chmod(filepath.Join(o.Root, "a.txt"), 0); err != nil {
		t.Fatal(err)
	}
	if st, err := o.Stat([]string{"."}, "a.txt"); err != nil || !st.Regular {
		t.Errorf("Stat = %+v, %v; a file that cannot be read still exists", st, err)
	}
	_, err := o.Open([]string{"."}, "a.txt")
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("Open = %v, want a permission error", err)
	}
}

// A refusal suggests the folder that holds the file, and no more than that:
// written from the project root where it is inside the root, from ~ where it
// is under home, and in full otherwise.
func TestSuggestedFolder(t *testing.T) {
	o, parent := machine(t)
	fix := func(o *OS, roots []string, path string) string {
		t.Helper()
		_, err := o.Stat(roots, path)
		b := blocked(err)
		if b == nil {
			t.Fatalf("Stat(%q, %q) = %v, want a refusal", roots, path, err)
		}
		if _, err := o.Open(roots, path); blocked(err) == nil || blocked(err).Fix != b.Fix {
			t.Errorf("Open(%q, %q) = %v, want the same suggestion as Stat: %q", roots, path, err, b.Fix)
		}
		if want := path + " is outside the folders the policy allows"; b.Why != want {
			t.Errorf("Stat(%q, %q): why = %q, want %q", roots, path, b.Why, want)
		}
		return strings.TrimPrefix(b.Fix, "holdfast allow files ")
	}
	tests := []struct{ path, want string }{
		// Inside the project: relative, the way "files" lines are written.
		{"sub/b.txt", "sub"},
		{"sub/deeper/still/x.txt", "sub/deeper/still"},
		{"./sub/../sub/b.txt", "sub"},
		{filepath.Join(parent, "project/sub/b.txt"), "sub"},
		{"no/such/file", "no/such"},
		{"my notes/todo.md", "'my notes'"},
		// Directly in the project folder: only then is the root itself named.
		{"a.txt", "."},
		{"./a.txt", "."},
		{"sub", "."},
		{".", "."},
		{filepath.Join(parent, "project"), "."},
		// Under home.
		{"~/docs/d.txt", "~/docs"},
		{"~/docs/a b/d.txt", "'~/docs/a b'"},
		{filepath.Join(parent, "home/docs/d.txt"), "~/docs"},
		{"~/notes.txt", "~"},
		{"~", "~"},
		// Anywhere else: in full when the check wrote it in full.
		{"/etc/passwd", "/etc"},
		{filepath.Join(parent, "outside/inner.txt"), filepath.Join(parent, "outside")},
		// A path written from the project is answered from the project, so the
		// refusal does not say where on the disk the project lies.
		{"../secret.txt", ".."},
		{"../outside/inner.txt", "../outside"},
		{"sub/../../outside/inner.txt", "../outside"},
		{"../home/docs/d.txt", "../home/docs"},
		{"..", "../.."},
	}
	for _, tt := range tests {
		got := fix(o, nil, tt.path)
		if got != tt.want {
			t.Errorf("for %q the suggestion is to allow %s, want %s", tt.path, got, tt.want)
		}
		if !filepath.IsAbs(tt.path) && strings.Contains(got, parent) {
			t.Errorf("for %q the suggestion gives away where the project is: %s", tt.path, got)
		}
	}
	// Following a suggestion that climbs out allows the file.
	if got, err := readAll(t, o, []string{"../outside"}, "../outside/inner.txt"); err != nil || got != secret {
		t.Errorf("after allowing ../outside: %q, %v", got, err)
	}
	refused(t, o, []string{"../outside"}, "../secret.txt")
	// It does not depend on what is on disk or on what else is allowed.
	if got := fix(o, []string{"sub", "~/docs"}, "other/x"); got != "other" {
		t.Errorf("with other folders allowed the suggestion is %s, want other", got)
	}

	// The store for the whole machine has the home folder as its root. A file
	// in a folder of home must not lead to a suggestion to open all of home.
	whole := &OS{Root: filepath.Join(parent, "home")}
	for _, tt := range []struct{ path, want string }{
		{"docs/d.txt", "docs"},
		{"~/docs/d.txt", "docs"},
		{filepath.Join(parent, "home/docs/d.txt"), "docs"},
		{".ssh/config", ".ssh"},
		{"notes.txt", "."},
		{"~/notes.txt", "."},
		{"~", "."},
		{"/etc/passwd", "/etc"},
	} {
		if got := fix(whole, nil, tt.path); got != tt.want {
			t.Errorf("whole machine: for %q the suggestion is to allow %s, want %s", tt.path, got, tt.want)
		}
	}
	// Following the suggestion allows the file, and not its neighbours above.
	if got, err := readAll(t, whole, []string{"docs"}, "~/docs/d.txt"); err != nil || got != "delta\n" {
		t.Errorf("after allowing docs: %q, %v", got, err)
	}
	refused(t, whole, []string{"docs"}, "notes.txt")
}

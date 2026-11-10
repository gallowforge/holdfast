//go:build unix

package world

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func run(t *testing.T, o *OS, max int, argv ...string) (int, string) {
	t.Helper()
	exit, out, err := o.Run(context.Background(), argv, max)
	if err != nil {
		t.Fatalf("Run(%q): %v", argv, err)
	}
	return exit, string(out)
}

func needs(t *testing.T, progs ...string) {
	t.Helper()
	for _, p := range progs {
		found := false
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			if st, err := os.Stat(filepath.Join(dir, p)); err == nil && !st.IsDir() {
				found = true
				break
			}
		}
		if !found {
			t.Skipf("%s is not installed here", p)
		}
	}
}

// The command is started directly. No shell reads the arguments.
func TestRunUsesNoShell(t *testing.T) {
	needs(t, "echo", "printf")
	o, _ := machine(t)
	exit, out := run(t, o, 1000, "echo", "$HOME", "*", ";", "id", "|", "cat", "`id`", "$(id)", "a  b", ">", "x")
	if exit != 0 || out != "$HOME * ; id | cat `id` $(id) a  b > x\n" {
		t.Errorf("echo = %d, %q", exit, out)
	}
	if _, err := os.Stat(filepath.Join(o.Root, "x")); err == nil {
		t.Error("a redirection in the arguments wrote a file")
	}
	// Each argument arrives as one argument, empty ones too.
	if _, out := run(t, o, 1000, "printf", "[%s]", "a b", "", "c"); out != "[a b][][c]" {
		t.Errorf("printf = %q", out)
	}
	// A program is a program, not a line to be split.
	if exit, out, err := o.Run(context.Background(), []string{"echo hello"}, 1000); err == nil {
		t.Errorf(`Run("echo hello") = %d, %q; want it not found`, exit, out)
	}
}

func TestRunEnvironmentAndFolder(t *testing.T) {
	needs(t, "env", "pwd")
	o, parent := machine(t)
	t.Setenv("HOLDFAST_TEST_TOKEN", secret)
	t.Setenv("AWS_SECRET_ACCESS_KEY", secret)
	t.Setenv("LANG", "de_DE.UTF-8")
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LD_PRELOAD", "")

	exit, out := run(t, o, 1<<20, "env")
	if exit != 0 || strings.Contains(out, secret) {
		t.Errorf("env = %d, %q", exit, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	sort.Strings(lines)
	want := []string{"HOME=" + filepath.Join(parent, "home"), "LANG=C", "LC_ALL=C", "PATH=" + os.Getenv("PATH")}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("the command saw this environment:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}

	if _, out := run(t, o, 1000, "pwd"); strings.TrimSpace(out) != o.Root {
		t.Errorf("pwd = %q, want the project folder %q", out, o.Root)
	}
}

func TestRunExitCodes(t *testing.T) {
	needs(t, "sh", "true", "false")
	o, _ := machine(t)
	tests := []struct {
		argv []string
		exit int
		out  string
	}{
		{[]string{"true"}, 0, ""},
		{[]string{"false"}, 1, ""},
		{[]string{"sh", "-c", "exit 3"}, 3, ""},
		{[]string{"sh", "-c", "exit 255"}, 255, ""},
		{[]string{"sh", "-c", "echo out; echo err >&2; exit 2"}, 2, "out\nerr\n"}, // both streams, in order
		{[]string{"sh", "-c", "echo only-err >&2"}, 0, "only-err\n"},
		{[]string{"sh", "-c", "cat"}, 0, ""}, // nothing to read on standard input
	}
	for _, tt := range tests {
		if exit, out := run(t, o, 1000, tt.argv...); exit != tt.exit || out != tt.out {
			t.Errorf("Run(%q) = %d, %q; want %d, %q", tt.argv, exit, out, tt.exit, tt.out)
		}
	}

	// A command that cannot be started is an error, not an exit code.
	script := filepath.Join(o.Root, "not-executable")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{{"holdfast-no-such-program-here"}, {"./missing"}, {script}, {o.Root}, {""}} {
		exit, out, err := o.Run(context.Background(), argv, 1000)
		if err == nil || exit != -1 || len(out) != 0 {
			t.Errorf("Run(%q) = %d, %q, %v; want an error", argv, exit, out, err)
		}
	}

	// A program in the project folder is run only when it is spelled as a path.
	tool := filepath.Join(o.Root, "tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho from-tool\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, out := run(t, o, 1000, "./tool"); out != "from-tool\n" {
		t.Errorf("./tool = %q", out)
	}
	if exit, out, err := o.Run(context.Background(), []string{"tool"}, 1000); err == nil {
		t.Errorf("a bare name ran a program from the project folder: %d, %q", exit, out)
	}
}

// A command stopped by a signal has no exit code. Run reports -1 and no
// error; this pins it.
func TestRunKilledBySignal(t *testing.T) {
	needs(t, "sh")
	o, _ := machine(t)
	if exit, out := run(t, o, 1000, "sh", "-c", "echo before; kill -9 $$"); exit != -1 || out != "before\n" {
		t.Errorf("a command that was killed = %d, %q; want -1", exit, out)
	}
}

func TestRunCapsOutput(t *testing.T) {
	needs(t, "head", "sh")
	o, _ := machine(t)
	for _, max := range []int{0, 1, 1000, 64 << 10} {
		exit, out := run(t, o, max, "head", "-c", "300000", "/dev/zero")
		if exit != 0 || len(out) != max {
			t.Errorf("with a limit of %d: exit %d, %d bytes kept", max, exit, len(out))
		}
	}
	// Under the limit, all of it. Both streams share the one limit.
	if _, out := run(t, o, 1000, "sh", "-c", "printf abc; printf def >&2"); out != "abcdef" {
		t.Errorf("out = %q", out)
	}
	if _, out := run(t, o, 4, "sh", "-c", "printf abc; printf def >&2"); out != "abcd" {
		t.Errorf("out = %q, want the first four bytes", out)
	}
	// Two streams written at once go through one writer; the race detector
	// watches this one.
	script := "i=0; while [ $i -lt 200 ]; do echo out-$i; echo err-$i >&2; i=$((i+1)); done"
	if exit, out := run(t, o, 1<<20, "sh", "-c", script); exit != 0 || strings.Count(out, "\n") != 400 {
		t.Errorf("exit %d, %d lines", exit, strings.Count(out, "\n"))
	}
}

func TestCapped(t *testing.T) {
	c := &capped{max: 5}
	for _, p := range []string{"ab", "", "cde", "fgh", "i"} {
		if n, err := c.Write([]byte(p)); n != len(p) || err != nil {
			t.Errorf("Write(%q) = %d, %v; a writer that says it wrote less stops the command", p, n, err)
		}
	}
	if string(c.buf) != "abcde" {
		t.Errorf("kept %q", c.buf)
	}
	c = &capped{max: 3}
	c.Write([]byte("abcdefgh"))
	if string(c.buf) != "abc" {
		t.Errorf("kept %q", c.buf)
	}
	for _, max := range []int{0, -1} {
		c = &capped{max: max}
		if n, err := c.Write([]byte("abc")); n != 3 || err != nil || len(c.buf) != 0 {
			t.Errorf("max %d: Write = %d, %v, kept %q", max, n, err, c.buf)
		}
	}
	// What is kept is a copy: the caller may reuse its buffer.
	c = &capped{max: 10}
	p := []byte("abc")
	c.Write(p)
	copy(p, "xyz")
	if !bytes.Equal(c.buf, []byte("abc")) {
		t.Errorf("kept %q", c.buf)
	}
}

// alive reports whether a process is still running. One that was killed and
// not yet collected by its parent does not count.
func alive(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	if runtime.GOOS == "linux" {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return false
		}
		// "pid (name) state ...": the state follows the last parenthesis.
		if i := bytes.LastIndexByte(stat, ')'); i >= 0 && i+2 < len(stat) {
			return stat[i+2] != 'Z' && stat[i+2] != 'X'
		}
	}
	return true
}

// When time runs out the command is stopped together with everything it
// started, and Run comes back at once rather than waiting for them.
func TestRunTimeoutStopsTheWholeFamily(t *testing.T) {
	needs(t, "sh", "sleep")
	o, _ := machine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	exit, out, err := o.Run(ctx, []string{"sh", "-c", "sleep 30 & echo $!; sleep 30 & echo $!; echo $$; sleep 30"}, 1000)
	took := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) || exit != -1 {
		t.Errorf("Run = %d, %q, %v; want the deadline", exit, out, err)
	}
	if took > 5*time.Second {
		t.Errorf("Run came back after %v with a 300ms deadline", took)
	}
	pids := strings.Fields(string(out))
	if len(pids) != 3 {
		t.Fatalf("output = %q, want three process numbers", out)
	}
	for i, s := range pids {
		pid, err := strconv.Atoi(s)
		if err != nil || pid <= 1 {
			t.Fatalf("output = %q", out)
		}
		if alive(pid) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("process %d of the command (%d) was still running after the timeout", i+1, pid)
		}
	}
	// The children held the output open. Had they lived, Run would have
	// waited out its two-second grace period for them.
	if runtime.GOOS == "linux" && took > 2*time.Second {
		t.Errorf("Run took %v: it waited for the children instead of stopping them", took)
	}
}

func TestRunWithAContextThatIsOver(t *testing.T) {
	needs(t, "sh")
	o, _ := machine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	marker := filepath.Join(o.Root, "ran")
	exit, out, err := o.Run(ctx, []string{"sh", "-c", "echo ran > ran"}, 1000)
	if !errors.Is(err, context.Canceled) || exit != -1 || len(out) != 0 {
		t.Errorf("Run = %d, %q, %v; want it not started", exit, out, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran although its context was over")
	}
}

// A command that finishes on time is not held up for long by something it
// left running in the background with the output still open: Run waits its
// two-second grace period and then reports the command's own result. This is
// the slowest test here.
func TestRunDoesNotWaitForAStraggler(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the two-second grace period")
	}
	needs(t, "sh", "sleep")
	o, _ := machine(t)
	start := time.Now()
	exit, out, err := o.Run(context.Background(), []string{"sh", "-c", "sleep 30 & echo $!"}, 1000)
	took := time.Since(start)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	if pid <= 1 {
		t.Fatalf("output = %q, want a process number", out)
	}
	// Run leaves the background process alone; the test cleans up after it.
	defer syscall.Kill(pid, syscall.SIGKILL)
	if exit != 0 || err != nil {
		t.Errorf("Run = %d, %q, %v; the command itself exited 0", exit, out, err)
	}
	if took > 8*time.Second {
		t.Errorf("Run waited %v for a background process", took)
	}
}

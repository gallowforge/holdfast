//go:build unix

package store

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
)

// The promise of a directory store is about processes, not goroutines: an
// agent, a person's terminal and a hook may all write to the same folder at
// the same moment. So the test binary starts copies of itself, and each copy
// is one of those processes.
const (
	childEnv = "HOLDFAST_TEST_CHILD" // the store folder; set only in a child
	roleEnv  = "HOLDFAST_TEST_ROLE"  // "writer" or "compactor"
	nameEnv  = "HOLDFAST_TEST_NAME"  // which writer this is
	countEnv = "HOLDFAST_TEST_N"     // how many facts a writer adds
	gateEnv  = "HOLDFAST_TEST_GATE"  // a folder the processes signal through
)

func TestMain(m *testing.M) {
	if dir := os.Getenv(childEnv); dir != "" {
		if err := child(dir); err != nil {
			fmt.Fprintln(os.Stderr, "child:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// waitFor polls until the file is there. Polling a file keeps the processes
// independent of each other: nothing is shared but the folder.
func waitFor(path string) error {
	deadline := time.Now().Add(30 * time.Second)
	for !exists(path) {
		if time.Now().After(deadline) {
			return fmt.Errorf("gave up waiting for %s", filepath.Base(path))
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

func child(dir string) error {
	gate, name := os.Getenv(gateEnv), os.Getenv(nameEnv)
	d, err := Open(dir, policy.Default())
	if err != nil {
		return err
	}
	defer d.Close()
	// Say "ready", then wait for "go", so every process starts writing at the
	// same moment instead of one after another.
	if err := os.WriteFile(filepath.Join(gate, "ready-"+name), nil, 0o644); err != nil {
		return err
	}
	if err := waitFor(filepath.Join(gate, "go")); err != nil {
		return err
	}

	if os.Getenv(roleEnv) == "compactor" {
		deadline := time.Now().Add(30 * time.Second)
		for n := 0; !exists(filepath.Join(gate, "stop")); n++ {
			if time.Now().After(deadline) {
				return fmt.Errorf("nobody said stop after %d compactions", n)
			}
			if _, _, err := d.Compact(); err != nil {
				return fmt.Errorf("Compact: %w", err)
			}
			time.Sleep(time.Millisecond)
		}
		return nil
	}

	n, err := strconv.Atoi(os.Getenv(countEnv))
	if err != nil {
		return err
	}
	check, err := fact.ParseCheck("file go.mod")
	if err != nil {
		return err
	}
	for i := range n {
		first := evidence(fact.Holds, at(time.Duration(i)*time.Second))
		f, err := d.Add(&fact.Fact{Claim: fmt.Sprintf("worker %s fact %d", name, i), Check: check.Clone(), By: "worker " + name, Created: t0}, &first)
		if err != nil {
			return fmt.Errorf("Add: %w", err)
		}
		if err := d.Record([]Proof{{ID: f.ID, Ev: evidence(fact.Broken, at(time.Hour))}}); err != nil {
			return fmt.Errorf("Record: %w", err)
		}
		got, err := d.Get(f.ID)
		if err != nil {
			return fmt.Errorf("reading back %s: %w", f.ID, err)
		}
		if got.Claim != f.Claim || got.Last == nil || got.Last.Status != fact.Broken {
			return fmt.Errorf("%s reads back as %q with evidence %+v", f.ID, got.Claim, got.Last)
		}
		if _, err := d.Facts(); err != nil {
			return fmt.Errorf("Facts: %w", err)
		}
	}
	if d.Damaged() != 0 {
		return fmt.Errorf("this process read %d damaged lines", d.Damaged())
	}
	return nil
}

func TestSeveralProcessesShareOneStore(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot find the test binary to run it again: %v", err)
	}
	const writers, each = 6, 20

	for _, tc := range []struct {
		name      string
		compactor bool
	}{
		{"all of them writing at once", false},
		{"while one more keeps compacting", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDir(t)
			gate := t.TempDir()

			type proc struct {
				name string
				cmd  *exec.Cmd
				out  *bytes.Buffer
				done chan error
			}
			start := func(role, name string) *proc {
				p := &proc{name: name, out: &bytes.Buffer{}, done: make(chan error, 1)}
				p.cmd = exec.Command(self)
				// The isolated HOME and HOLDFAST_STATE come along with the rest.
				p.cmd.Env = append(os.Environ(),
					childEnv+"="+d.Path(), roleEnv+"="+role, nameEnv+"="+name,
					countEnv+"="+strconv.Itoa(each), gateEnv+"="+gate)
				p.cmd.Stdout, p.cmd.Stderr = p.out, p.out
				if err := p.cmd.Start(); err != nil {
					t.Fatalf("starting %s: %v", name, err)
				}
				go func() { p.done <- p.cmd.Wait() }()
				t.Cleanup(func() { p.cmd.Process.Kill() }) // no child outlives the test
				return p
			}
			finish := func(p *proc) {
				t.Helper()
				select {
				case err := <-p.done:
					if err != nil {
						t.Errorf("process %s failed: %v\n%s", p.name, err, p.out)
					}
				case <-time.After(60 * time.Second):
					t.Fatalf("process %s did not finish\n%s", p.name, p.out)
				}
			}

			var procs []*proc
			for w := range writers {
				procs = append(procs, start("writer", strconv.Itoa(w)))
			}
			var compactor *proc
			if tc.compactor {
				compactor = start("compactor", "compactor")
			}
			for _, p := range append(procs[:len(procs):len(procs)], compactor) {
				if p == nil {
					continue
				}
				if err := waitFor(filepath.Join(gate, "ready-"+p.name)); err != nil {
					t.Fatalf("process %s never got ready: %v\n%s", p.name, err, p.out)
				}
			}
			writeFile(t, filepath.Join(gate, "go"), "")
			for _, p := range procs {
				finish(p)
			}
			if compactor != nil {
				writeFile(t, filepath.Join(gate, "stop"), "")
				finish(compactor)
			}
			if t.Failed() {
				return
			}

			wholeAndComplete(t, d.Path(), writers, each)
			// The handle that was open all along catches up with all of it.
			if all := mustFacts(t, d); len(all) != writers*each || d.Damaged() != 0 {
				t.Errorf("the handle opened before the others started reads %d facts and %d damaged lines", len(all), d.Damaged())
			}
			by := map[string]int{}
			for _, f := range mustFacts(t, d) {
				by[f.By]++
			}
			for w := range writers {
				if n := by["worker "+strconv.Itoa(w)]; n != each {
					t.Errorf("writer %d has %d facts in the store, want %d", w, n, each)
				}
			}
		})
	}
}

package engine

import (
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/sim"
)

// The first thing anyone does with a memory like this is store a fact, break
// it, and ask. A file check costs nothing to run again, so the answer must be
// right even a moment after the last proof.
func TestRecallAlwaysReadsFilesAgain(t *testing.T) {
	checks := []struct {
		name  string
		check string
		spoil func(w *sim.World, at time.Time)
	}{
		{"contains", `contains config/app.yaml "port: 8080"`, func(w *sim.World, at time.Time) {
			w.Write("config/app.yaml", "port: 9090\n", at)
		}},
		{"matches", `matches config/app.yaml "^mode: production$"`, func(w *sim.World, at time.Time) {
			w.Write("config/app.yaml", "port: 8080\nmode: staging\n", at)
		}},
		{"unchanged", "unchanged go.sum", func(w *sim.World, at time.Time) {
			w.Write("go.sum", "example.com/dep v1.0.1 h1:def=\n", at)
		}},
		{"file", "file go.sum", func(w *sim.World, at time.Time) {
			delete(w.Files, "go.sum")
		}},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t)
			b.remember("A thing about the project's files", tc.check)
			b.later(time.Second)
			at := b.clock.now()
			b.change(func(w *sim.World) { tc.spoil(w, at) })

			v := b.recall("files", 0).Facts[0]
			if v.Status != fact.Broken {
				t.Fatalf("status = %s a second after the file changed, want broken", v.Status)
			}
			if !v.Fresh {
				t.Error("the answer is not marked as proved just now")
			}
		})
	}
}

// A fact the policy turned away is looked at again on every recall: the only
// thing that changes its answer is a person allowing it, and the agent should
// see that the moment it happens, not half a minute later.
func TestRecallRunsABlockedCheckOnceItIsAllowed(t *testing.T) {
	b := newBench(t)
	b.change(func(w *sim.World) { w.Cmds["docker info"] = sim.Result{} })
	v := b.remember("Docker is running on this machine", "cmd docker info")
	if v.Status != fact.Blocked {
		t.Fatalf("status = %s, want blocked: the bench policy does not allow that command", v.Status)
	}

	b.later(time.Second)
	b.store.SetPolicy(rules(t, benchPolicy+"cmd docker info\n"))

	got := b.recall("docker", 0).Facts[0]
	if got.Status != fact.Holds {
		t.Fatalf("status = %s a second after a person allowed the check, want holds", got.Status)
	}
}

// Package demo is the pretend project behind the live demo: a small service
// called atlas, the machine it runs on, and what three agents and one person
// have remembered about it. Everything is invented, and the facts are proved
// against the pretend machine by the same engine that proves real ones.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/gallowforge/holdfast/internal/api"
	"github.com/gallowforge/holdfast/internal/engine"
	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/proof"
	"github.com/gallowforge/holdfast/internal/sim"
	"github.com/gallowforge/holdfast/internal/store"
)

const (
	day  = 24 * time.Hour
	base = `files .
net loopback 192.168.1.20 staging.example.com
cmd systemctl is-active *
`
	flagsBefore = "{\n  \"new_checkout\": false,\n  \"rate_limit\": 120\n}\n"
	flagsAfter  = "{\n  \"new_checkout\": true,\n  \"rate_limit\": 120\n}\n"
	gomod       = "module example.com/atlas\n\ngo %s\n\nrequire example.com/router v1.8.2\n"
	makefile    = ".PHONY: build test %[1]s\n\nbuild:\n\tgo build -o bin/atlas ./cmd/atlas\n\ntest:\n\tgo test ./...\n\n%[1]s:\n\t./scripts/release.sh\n"
)

// Switch is one thing a visitor can change on the pretend machine.
type Switch struct {
	ID string `json:"id"`
	// Group is "machine" for things that happen to a project, and "policy"
	// for the one decision that belongs to a person.
	Group string `json:"group"`
	// Do and Undo label the switch in its two positions.
	Do   string `json:"do"`
	Undo string `json:"undo"`
	// What says, in the machine's own terms, what changes.
	What string `json:"what"`
	On   bool   `json:"on"`
	// Facts are the ids of the remembered facts the change bears on.
	Facts []string `json:"facts"`

	set func(d *Demo, on bool)
}

// Note is one line of the plain notes file the demo compares against: what a
// memory without proof goes on saying whatever happens to the machine.
type Note struct {
	Question string `json:"question"`
	Query    string `json:"query"`
	Line     string `json:"line"`
	Fact     string `json:"fact"`
}

// Machine is what the demo's side panel shows.
type Machine struct {
	Switches []Switch `json:"switches"`
	Notes    []Note   `json:"notes"`
	// Written is when the notes file was last touched.
	Written time.Time `json:"written"`
}

// Demo is one visitor's pretend project.
type Demo struct {
	World  *sim.World
	Store  *store.Memory
	Engine *engine.Engine

	mu       sync.Mutex
	start    time.Time
	switches []Switch
}

// Ids of the seeded facts.
const (
	fPort    = "f-7k2m9x"
	fHealth  = "f-c4hd8p"
	fGo      = "f-9wq3zn"
	fRelease = "f-m6t2ya"
	fStaging = "f-x8r5kb"
	fDB      = "f-3ndp7g"
	fFlags   = "f-q9s4vh"
	fBackup  = "f-h2j8wc"
	fDocker  = "f-t5y3mk"
	fLegacy  = "f-b7g6rz"
	fBranch  = "f-k4x9ns"
	fRefresh = "f-w3c8dq"
	fBilling = "f-z6p2tj"
)

type seed struct {
	id, claim, check, by string
	tags                 []string
	age                  time.Duration
}

var seeds = []seed{
	{fPort, "The API listens on port 8080", "port localhost:8080", "claude-code", []string{"api"}, 12 * day},
	{fHealth, "The health endpoint answers 200 with ok", "http 200 http://localhost:8080/healthz ok", "claude-code", []string{"api"}, 12 * day},
	// The tag "version" is what makes this fact, and not the note about
	// where deploys "go out" from, the answer to the question "go version".
	{fGo, "Go is pinned to 1.24", `contains go.mod "go 1.24"`, "cursor", []string{"build", "version"}, 9 * day},
	{fRelease, "Releases are cut with make release", `matches Makefile "^release:"`, "alex", []string{"build", "release"}, 20 * day},
	{fStaging, "Staging is up at staging.example.com", "http 200 https://staging.example.com/healthz", "codex", []string{"deploy"}, 16 * day},
	{fDB, "Postgres runs on 192.168.1.20, port 5432", "port 192.168.1.20:5432", "claude-code", []string{"database"}, 12 * day},
	{fFlags, "The feature flags have not changed since the audit", "unchanged config/flags.json", "alex", []string{"config"}, 7 * day},
	{fBackup, "A backup was written in the last 26 hours", "fresh backups/latest.tar.zst 26h", "codex", []string{"backup"}, 15 * day},
	{fDocker, "Docker is running on this machine", "says active systemctl is-active docker", "cursor", []string{"deploy"}, 9 * day},
	{fLegacy, "The legacy config file is gone for good", "not file config/legacy.yml", "claude-code", []string{"config"}, 4 * day},
	{fBilling, "The billing service answers from this machine", "http 200 https://billing.example.net/ping", "claude-code", []string{"api"}, 2 * day},
	{fBranch, "Only the main branch is deployed", "", "alex", []string{"deploy"}, 6 * day},
	{fRefresh, "The staging database is refreshed every Sunday night", "", "cursor", []string{"database"}, 41 * day},
}

var notes = []Note{
	{"Which port does the API listen on?", "api port", "The API listens on port 8080.", fPort},
	{"Which Go version does the project use?", "go version", "Go is pinned to 1.24.", fGo},
	{"How is a release cut?", "release", "Releases are cut with make release.", fRelease},
	{"Is staging up?", "staging up", "Staging is up at staging.example.com.", fStaging},
	{"Is there a recent backup?", "backup", "A backup is written every night.", fBackup},
	{"Where does Postgres run?", "postgres", "Postgres runs on 192.168.1.20, port 5432.", fDB},
}

func switches() []Switch {
	return []Switch{
		{ID: "api", Group: "machine", Do: "Stop the API", Undo: "Start the API again",
			What: "nothing listens on localhost:8080", Facts: []string{fPort, fHealth},
			set: func(d *Demo, on bool) { d.World.Ports["localhost:8080"] = !on }},
		{ID: "go", Group: "machine", Do: "Upgrade Go to 1.25", Undo: "Go back to Go 1.24",
			What: "go.mod says go 1.25", Facts: []string{fGo},
			set: func(d *Demo, on bool) {
				v := "1.24"
				if on {
					v = "1.25"
				}
				d.World.Write("go.mod", fmt.Sprintf(gomod, v), d.World.Now())
			}},
		{ID: "release", Group: "machine", Do: "Rename the release target", Undo: "Name it release again",
			What: "the Makefile target is now called ship", Facts: []string{fRelease},
			set: func(d *Demo, on bool) {
				name := "release"
				if on {
					name = "ship"
				}
				d.World.Write("Makefile", fmt.Sprintf(makefile, name), d.World.Now())
			}},
		{ID: "staging", Group: "machine", Do: "Take staging down", Undo: "Bring staging back",
			What: "staging.example.com answers 503", Facts: []string{fStaging},
			set: func(d *Demo, on bool) {
				p := sim.Page{Status: 200, Body: `{"status":"ok"}`}
				if on {
					p = sim.Page{Status: 503, Body: "upstream unavailable"}
				}
				d.World.Pages["https://staging.example.com/healthz"] = p
			}},
		{ID: "db", Group: "machine", Do: "Move the database", Undo: "Move the database back",
			What: "nothing listens on 192.168.1.20:5432", Facts: []string{fDB},
			set: func(d *Demo, on bool) { d.World.Ports["192.168.1.20:5432"] = !on }},
		{ID: "flags", Group: "machine", Do: "Edit the feature flags", Undo: "Revert the feature flags",
			What: "config/flags.json turns new_checkout on", Facts: []string{fFlags},
			set: func(d *Demo, on bool) {
				text := flagsBefore
				if on {
					text = flagsAfter
				}
				d.World.Write("config/flags.json", text, d.World.Now())
			}},
		{ID: "backup", Group: "machine", Do: "Skip last night's backup", Undo: "Run the backup",
			What: "backups/latest.tar.zst is 31 hours old", Facts: []string{fBackup},
			set: func(d *Demo, on bool) {
				age := 3 * time.Hour
				if on {
					age = 31 * time.Hour
				}
				d.World.Write("backups/latest.tar.zst", "(archive)", d.World.Now().Add(-age))
			}},
		{ID: "docker", Group: "machine", Do: "Stop Docker", Undo: "Start Docker",
			What: "systemctl says docker is inactive", Facts: []string{fDocker},
			set: func(d *Demo, on bool) {
				r := sim.Result{Exit: 0, Out: "active\n"}
				if on {
					r = sim.Result{Exit: 3, Out: "inactive\n"}
				}
				d.World.Cmds["systemctl is-active docker"] = r
			}},
		{ID: "legacy", Group: "machine", Do: "Restore the legacy config", Undo: "Delete the legacy config",
			What: "config/legacy.yml is back", Facts: []string{fLegacy},
			set: func(d *Demo, on bool) {
				if on {
					d.World.Write("config/legacy.yml", "mode: legacy\n", d.World.Now())
				} else {
					delete(d.World.Files, "config/legacy.yml")
				}
			}},
		{ID: "billing", Group: "policy", Do: "Allow billing.example.net", Undo: "Take that back",
			What: "the policy gains: net billing.example.net", Facts: []string{fBilling},
			set: func(d *Demo, on bool) {
				text := base
				if on {
					text += "net billing.example.net\n"
				}
				d.Store.SetPolicy(mustPolicy(text))
			}},
	}
}

func mustPolicy(text string) *policy.Policy {
	p, err := policy.Parse(text)
	if err != nil {
		panic("demo: " + err.Error())
	}
	return p
}

// New builds the project as it stands when a visitor arrives: thirteen facts
// with a past, every one of them true.
func New(clock func() time.Time) *Demo {
	d := &Demo{World: sim.New(clock), start: clock(), switches: switches()}
	d.Store = store.NewMemory(mustPolicy(base))
	d.Engine = &engine.Engine{Store: d.Store, World: d.World}

	w := d.World
	w.Names["staging.example.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.10")}
	w.Names["billing.example.net"] = []netip.Addr{netip.MustParseAddr("198.51.100.7")}
	w.Ports["staging.example.com:443"] = true
	w.Ports["billing.example.net:443"] = true
	w.Pages["http://localhost:8080/healthz"] = sim.Page{Status: 200, Body: "ok"}
	w.Pages["https://billing.example.net/ping"] = sim.Page{Status: 200, Body: "pong"}
	w.Write("README.md", "# atlas\n\nA small service that does not exist.\n", d.start.Add(-30*day))
	for i := range d.switches {
		d.switches[i].set(d, false)
	}

	ctx := context.Background()
	rules, _ := d.Store.Policy()
	for _, s := range seeds {
		made := d.start.Add(-s.age)
		f := &fact.Fact{ID: s.id, Claim: s.claim, By: s.by, Tags: s.tags, Created: made}
		if s.check == "" {
			d.Store.Seed(store.Event{T: made, Op: "add", ID: s.id, Fact: f})
			continue
		}
		c, err := fact.ParseCheck(s.check)
		if err != nil {
			panic("demo: " + s.check + ": " + err.Error())
		}
		if err := proof.Seal(ctx, w, rules, c); err != nil {
			panic("demo: " + s.check + ": " + err.Error())
		}
		f.Check = c
		d.Store.Seed(store.Event{T: made, Op: "add", ID: s.id, Fact: f})
		// The fact is proved now and the evidence dated back, so the ledger
		// opens with a history and the words are the engine's own.
		d.prove(s.id, c, made)
		if s.id == fStaging {
			// Staging had a bad afternoon five days ago.
			d.flip("staging", true)
			d.prove(s.id, c, d.start.Add(-5*day-3*time.Hour))
			d.flip("staging", false)
			d.prove(s.id, c, d.start.Add(-5*day-50*time.Minute))
		}
		d.prove(s.id, c, d.start.Add(-7*time.Minute))
	}
	return d
}

func (d *Demo) prove(id string, c *fact.Check, at time.Time) {
	rules, _ := d.Store.Policy()
	ev := proof.Run(context.Background(), d.World, rules, c)
	ev.At = at
	d.Store.Seed(store.Event{T: at, Op: "evidence", ID: id, Ev: &ev})
}

func (d *Demo) flip(id string, on bool) bool {
	for i := range d.switches {
		if s := &d.switches[i]; s.ID == id {
			d.World.Change(func(*sim.World) { s.set(d, on) })
			s.On = on
			return true
		}
	}
	return false
}

// Machine reports the switches as they stand.
func (d *Demo) Machine() Machine {
	d.mu.Lock()
	defer d.mu.Unlock()
	return Machine{
		Switches: append([]Switch(nil), d.switches...),
		Notes:    notes,
		Written:  d.start.Add(-12 * day),
	}
}

// More answers the calls only the demo has.
func (d *Demo) More(_ context.Context, method string, raw json.RawMessage) (any, bool, error) {
	switch method {
	case "machine":
		return d.Machine(), true, nil
	case "flip":
		var in struct {
			ID string `json:"id"`
		}
		json.Unmarshal(raw, &in)
		d.mu.Lock()
		ok := false
		for _, s := range d.switches {
			if s.ID == in.ID {
				ok = d.flip(s.ID, !s.On)
			}
		}
		d.mu.Unlock()
		if !ok {
			return nil, true, &engine.Refused{Why: "there is no such switch"}
		}
		return d.Machine(), true, nil
	case "reset":
		d.mu.Lock()
		for _, s := range d.switches {
			if s.On {
				d.flip(s.ID, false)
			}
		}
		d.mu.Unlock()
		return d.Machine(), true, nil
	}
	return nil, false, nil
}

// API is the door the demo's page comes through.
func (d *Demo) API(version string) *api.API {
	return &api.API{
		Engine:  d.Engine,
		Version: version,
		Who:     "you",
		More:    d.More,
		Where: func() api.Where {
			rules, _ := d.Store.Policy()
			return api.Where{Project: "atlas", Policy: rules.String(), Approved: true}
		},
	}
}

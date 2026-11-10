package main

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/store"
)

// where says which store a command is about.
type where struct {
	dir    string // --dir or HOLDFAST_DIR: a project folder, or the store folder itself
	global bool   // --global: the store for the whole machine, in the home folder
}

// init is the command that creates the store w is about. It is what a hint
// must name: plain "holdfast init" would create a different store, in the
// folder the person happens to stand in.
func (w where) init() string {
	switch {
	case w.global:
		return "holdfast init --global"
	case w.dir != "":
		return "holdfast init --dir " + fact.Quote(w.dir)
	}
	return "holdfast init"
}

// noStore is the error for a folder that has no store yet.
type noStore string

func (e noStore) Error() string { return string(e) }

// home is the user's home folder with links resolved, so it compares equal
// to a store's root.
func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(h); err == nil {
		return real
	}
	return h
}

// basePolicy is what a store may touch before a person says otherwise. A
// project's checks may read the project. The store for the whole machine
// starts without any folder: its root is the home folder, and that holds
// keys, tokens and mail that no check should read until a person names the
// folders that matter.
func basePolicy(storePath string) *policy.Policy {
	root := filepath.Dir(storePath)
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if h := home(); h != "" && root == h {
		return &policy.Policy{Net: []policy.NetRule{{Class: "loopback"}}}
	}
	return policy.Default()
}

// path works out where the store is, or would be. found is false when
// nothing is there yet.
func (a *app) path(w where) (path string, found bool, err error) {
	switch {
	case w.global:
		h := home()
		if h == "" {
			return "", false, errors.New("cannot find your home folder")
		}
		path = filepath.Join(h, store.DirName)
	case w.dir != "":
		path = w.dir
		if filepath.Base(path) != store.DirName {
			path = filepath.Join(path, store.DirName)
		}
		if path, err = filepath.Abs(path); err != nil {
			return "", false, err
		}
	default:
		if p, ok := store.Find(a.cwd); ok {
			return p, true, nil
		}
		return filepath.Join(a.cwd, store.DirName), false, nil
	}
	st, err := os.Stat(path)
	return path, err == nil && st.IsDir(), nil
}

// open opens the store a command is about.
func (a *app) open(w where) (*store.Dir, error) {
	path, found, err := a.path(w)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, noStore("there is no Holdfast store here (looked for " + store.DirName + " in this folder and every folder above it). Create one with: " + w.init())
	}
	return store.Open(path, basePolicy(path))
}

// lazy is the store behind an agent that was started in a folder with no
// store. Reading from it says so; the first fact stored creates it, with the
// default policy, exactly as "holdfast init" would.
type lazy struct {
	path string
	make bool   // whether storing the first fact may create the store
	init string // the command a person would create it with

	mu sync.Mutex
	d  *store.Dir
}

func (l *lazy) get(create bool) (*store.Dir, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.d != nil {
		return l.d, nil
	}
	st, err := os.Stat(l.path)
	exists := err == nil && st.IsDir()
	switch {
	case exists:
		l.d, err = store.Open(l.path, basePolicy(l.path))
	case create && l.make:
		l.d, err = store.Init(l.path, basePolicy(l.path))
	default:
		return nil, nil
	}
	return l.d, err
}

func (l *lazy) Facts() ([]*fact.Fact, error) {
	d, err := l.get(false)
	if d == nil {
		return nil, err
	}
	return d.Facts()
}

func (l *lazy) Get(id string) (*fact.Fact, error) {
	d, err := l.get(false)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, store.ErrNotFound
	}
	return d.Get(id)
}

func (l *lazy) Add(f *fact.Fact, ev *fact.Evidence) (*fact.Fact, error) {
	d, err := l.get(true)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, &store.Rejected{Why: "there is no Holdfast store in " + filepath.Dir(l.path) + " yet, and this server was told not to create one. A person can create it: " + l.init}
	}
	return d.Add(f, ev)
}

func (l *lazy) Record(batch []store.Proof) error {
	d, err := l.get(false)
	if d == nil {
		return err
	}
	return d.Record(batch)
}

func (l *lazy) Retract(id string, r fact.Retraction) error {
	d, err := l.get(false)
	if err != nil {
		return err
	}
	if d == nil {
		return store.ErrNotFound
	}
	return d.Retract(id, r)
}

func (l *lazy) Vouch(id, by string, at time.Time) error {
	d, err := l.get(false)
	if err != nil {
		return err
	}
	if d == nil {
		return store.ErrNotFound
	}
	return d.Vouch(id, by, at)
}

func (l *lazy) Policy() (*policy.Policy, string) {
	d, _ := l.get(false)
	if d == nil {
		return basePolicy(l.path), ""
	}
	return d.Policy()
}

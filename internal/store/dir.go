//go:build unix

package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
)

// Names inside a store folder.
const (
	DirName      = ".holdfast"
	FactsFile    = "facts.jsonl"
	EvidenceFile = "evidence.jsonl"
	PolicyFile   = "policy"
	lockFile     = "lock"
)

const maxJournalRead = 256 << 20

// Dir is a store kept in a .holdfast folder. Any number of processes may use
// the same folder at once: appends are made under an exclusive file lock, and
// every read first catches up with what other processes have written.
type Dir struct {
	path string
	base *policy.Policy

	mu       sync.Mutex
	lock     *os.File
	st       *state
	facts    tail
	evidence tail

	// Rand supplies randomness for ids; nil means the system's.
	Rand io.Reader
}

var _ Store = (*Dir)(nil)

// tail remembers how far into a journal this process has read.
type tail struct {
	name string
	off  int64
	info os.FileInfo
	mark []byte // the bytes just before off, to notice a file rewritten in place
}

// Find walks up from start to the nearest folder that holds a store, and
// returns the path of the store folder.
func Find(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	for {
		p := filepath.Join(dir, DirName)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Init creates a store folder (or completes one that is missing files) and
// approves its policy. base is the policy a new store starts with.
func Init(path string, base *policy.Policy) (*Dir, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, err
	}
	create := func(name, content string) error {
		f, err := os.OpenFile(filepath.Join(path, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString(content)
		return err
	}
	fresh := false
	if _, err := os.Stat(filepath.Join(path, PolicyFile)); errors.Is(err, fs.ErrNotExist) {
		fresh = true
	}
	for name, content := range map[string]string{
		PolicyFile:   base.String(),
		FactsFile:    "",
		EvidenceFile: "",
		// Facts and the policy are meant to be committed; proof is not.
		".gitignore":     EvidenceFile + "\n" + lockFile + "\n",
		".gitattributes": FactsFile + " merge=union\n",
	} {
		if err := create(name, content); err != nil {
			return nil, err
		}
	}
	d, err := Open(path, base)
	if err != nil {
		return nil, err
	}
	if fresh {
		if err := d.Trust(); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// Open opens an existing store folder. base is the policy in force while the
// policy file is missing or has not been approved on this machine.
func Open(path string, base *policy.Policy) (*Dir, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", path)
	}
	lock, err := os.OpenFile(filepath.Join(path, lockFile), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	return &Dir{
		path: path, base: base, lock: lock, st: newState(),
		facts: tail{name: FactsFile}, evidence: tail{name: EvidenceFile},
	}, nil
}

// Close releases the store's lock file.
func (d *Dir) Close() error { return d.lock.Close() }

// Path is the store folder; Root is the folder it describes.
func (d *Dir) Path() string { return d.path }
func (d *Dir) Root() string { return filepath.Dir(d.path) }

// Damaged counts journal lines that could not be read.
func (d *Dir) Damaged() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st.damaged
}

// locked runs fn holding the file lock, after catching up with the journals.
func (d *Dir) locked(exclusive bool, fn func() error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	for {
		err := syscall.Flock(int(d.lock.Fd()), how)
		if err == nil {
			break
		}
		if err != syscall.EINTR {
			return fmt.Errorf("locking %s: %w", d.path, err)
		}
	}
	defer syscall.Flock(int(d.lock.Fd()), syscall.LOCK_UN)
	if err := d.catchUp(); err != nil {
		return err
	}
	return fn()
}

// catchUp reads whatever the journals have gained since the last look. A
// journal that was replaced, cut short or rewritten (compaction, a git
// checkout, an editor) is read again from the start.
func (d *Dir) catchUp() error {
	for _, t := range []*tail{&d.facts, &d.evidence} {
		if !t.intact(d.path) {
			d.st = newState()
			d.facts = tail{name: FactsFile}
			d.evidence = tail{name: EvidenceFile}
			break
		}
	}
	if err := d.read(&d.facts); err != nil {
		return err
	}
	return d.read(&d.evidence)
}

func (t *tail) intact(dir string) bool {
	if t.info == nil {
		return true
	}
	f, err := os.Open(filepath.Join(dir, t.name))
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(t.info, info) || info.Size() < t.off {
		return false
	}
	if len(t.mark) > 0 {
		got := make([]byte, len(t.mark))
		if _, err := f.ReadAt(got, t.off-int64(len(t.mark))); err != nil || !bytes.Equal(got, t.mark) {
			return false
		}
	}
	return true
}

func (d *Dir) read(t *tail) error {
	f, err := os.Open(filepath.Join(d.path, t.name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	t.info = info
	n := info.Size() - t.off
	if n <= 0 {
		return nil
	}
	if n > maxJournalRead {
		return fmt.Errorf("%s is too large to read", t.name)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(io.NewSectionReader(f, t.off, n), buf); err != nil {
		return err
	}
	// Only whole lines count. A last line with no newline is a write that was
	// cut off; it is left alone, and the next append closes it off so it
	// becomes one damaged line instead of corrupting its neighbour.
	end := bytes.LastIndexByte(buf, '\n') + 1
	for _, line := range bytes.Split(buf[:end], []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			d.st.damaged++
			continue
		}
		isEvidence := e.Op == "evidence"
		switch {
		case isEvidence != (t.name == EvidenceFile):
			d.st.damaged++ // an event in the wrong journal
		case e.Op == "add" && d.st.facts[e.ID] != nil:
			// The same line twice (a merge kept both copies): harmless.
		case !d.st.apply(e) && !isEvidence:
			// Evidence for a fact this machine no longer has is simply
			// skipped; anything else that does not apply is damage.
			d.st.damaged++
		}
	}
	t.off += int64(end)
	if end > 0 {
		t.mark = append([]byte(nil), buf[max(0, end-64):end]...)
	}
	return nil
}

// write appends events to one journal as a single write, then syncs it.
func (d *Dir) write(name string, events ...Event) error {
	var buf bytes.Buffer
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	f, err := os.OpenFile(filepath.Join(d.path, name), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	data := buf.Bytes()
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, info.Size()-1); err == nil && last[0] != '\n' {
			data = append([]byte("\n"), data...) // close off a line that was cut short
		}
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func (d *Dir) Facts() ([]*fact.Fact, error) {
	var out []*fact.Fact
	err := d.locked(false, func() error {
		out = d.st.all()
		return nil
	})
	return out, err
}

func (d *Dir) Get(id string) (*fact.Fact, error) {
	var out *fact.Fact
	err := d.locked(false, func() error {
		f := d.st.facts[id]
		if f == nil {
			return ErrNotFound
		}
		out = f.Clone()
		return nil
	})
	return out, err
}

func (d *Dir) Add(f *fact.Fact, ev *fact.Evidence) (*fact.Fact, error) {
	var out *fact.Fact
	err := d.locked(true, func() error {
		if old := d.st.twin(f); old != nil {
			out = old.Clone()
			return ErrAlready
		}
		if err := d.st.admit(f); err != nil {
			return err
		}
		id, err := d.st.newID(d.Rand)
		if err != nil {
			return err
		}
		claimed := &fact.Fact{
			ID: id, Claim: f.Claim, Tags: f.Tags, Check: f.Check, By: f.By,
			Created: f.Created, TTL: f.TTL, Replaces: f.Replaces,
		}
		if err := d.write(FactsFile, Event{T: f.Created, Op: "add", ID: id, Fact: claimed}); err != nil {
			return err
		}
		if ev != nil {
			if err := d.write(EvidenceFile, Event{T: ev.At, Op: "evidence", ID: id, Ev: ev}); err != nil {
				return err
			}
		}
		if err := d.catchUp(); err != nil {
			return err
		}
		stored := d.st.facts[id]
		if stored == nil {
			return errors.New("the fact was written but could not be read back")
		}
		out = stored.Clone()
		return nil
	})
	return out, err
}

func (d *Dir) Record(batch []Proof) error {
	if len(batch) == 0 {
		return nil
	}
	return d.locked(true, func() error {
		var events []Event
		for _, p := range batch {
			f := d.st.facts[p.ID]
			if f == nil || f.Check == nil {
				continue
			}
			if d.st.worthWriting(f, p.Ev) {
				ev := p.Ev
				events = append(events, Event{T: ev.At, Op: "evidence", ID: p.ID, Ev: &ev})
			} else {
				d.st.prove(f, p.Ev) // remembered by this process only
			}
		}
		if len(events) == 0 {
			return nil
		}
		if err := d.write(EvidenceFile, events...); err != nil {
			return err
		}
		return d.catchUp()
	})
}

func (d *Dir) Retract(id string, r fact.Retraction) error {
	return d.locked(true, func() error {
		if d.st.facts[id] == nil {
			return ErrNotFound
		}
		if err := d.write(FactsFile, Event{T: r.At, Op: "retract", ID: id, By: r.By, Reason: r.Reason}); err != nil {
			return err
		}
		return d.catchUp()
	})
}

func (d *Dir) Vouch(id, by string, at time.Time) error {
	return d.locked(true, func() error {
		if d.st.facts[id] == nil {
			return ErrNotFound
		}
		if err := d.write(FactsFile, Event{T: at, Op: "vouch", ID: id, By: by}); err != nil {
			return err
		}
		return d.catchUp()
	})
}

// Compact rewrites the evidence journal so it holds only what still matters:
// each fact's changes of state and its latest evidence. It returns the size
// of the journal before and after.
func (d *Dir) Compact() (before, after int64, err error) {
	err = d.locked(true, func() error {
		path := filepath.Join(d.path, EvidenceFile)
		if info, err := os.Stat(path); err == nil {
			before = info.Size()
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		put := func(id string, ev fact.Evidence) {
			enc.Encode(Event{T: ev.At, Op: "evidence", ID: id, Ev: &ev})
		}
		for _, id := range d.st.order {
			f := d.st.facts[id]
			if f.Last == nil {
				continue
			}
			// A fact that held once and has changed state more often since than
			// the history keeps has no passing check left in its history. Its
			// "last held" is written first, as the oldest change: reading the
			// journal back, the history's limit drops it again and keeps the time.
			held := false
			for _, ev := range f.History {
				held = held || ev.Status == fact.Holds
			}
			if !held && !f.Held.IsZero() {
				put(id, fact.Evidence{Status: fact.Holds, At: f.Held})
			}
			for i, ev := range f.History {
				put(id, ev)
				// The last time the check passed is kept even when it is not a
				// change of state, so "last held" survives compaction.
				lastHolds := ev.Status == fact.Holds
				for _, later := range f.History[i+1:] {
					if later.Status == fact.Holds {
						lastHolds = false
					}
				}
				if lastHolds && f.Held.After(ev.At) && f.Last.Status != fact.Holds {
					still := ev
					still.At = f.Held
					put(id, still)
				}
			}
			if n := len(f.History); n == 0 || !f.History[n-1].At.Equal(f.Last.At) {
				put(id, *f.Last)
			}
		}
		tmp, err := os.CreateTemp(d.path, EvidenceFile+".*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.Write(buf.Bytes()); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.Chmod(tmp.Name(), 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			return err
		}
		after = int64(buf.Len())
		return d.catchUp()
	})
	return before, after, err
}

// Policy returns the rules in force. The policy file only counts once a
// person has approved it on this machine: a policy that arrived with a cloned
// repository, or that something edited, allows nothing extra until then.
func (d *Dir) Policy() (*policy.Policy, string) {
	data, err := os.ReadFile(filepath.Join(d.path, PolicyFile))
	if errors.Is(err, fs.ErrNotExist) {
		return d.base, ""
	}
	if err != nil {
		return &policy.Policy{}, "the policy file cannot be read, so no check may touch anything until it can"
	}
	p, err := policy.Parse(string(data))
	if err != nil {
		return &policy.Policy{}, "the policy file has a mistake (" + err.Error() + "), so no check may touch anything until it is fixed"
	}
	if !d.approved(data) && !d.grantsNothing(p) {
		return d.base, "the policy file has changed and nobody has approved it on this machine, so only the defaults apply. A person can read " +
			filepath.Join(DirName, PolicyFile) + " and approve it: holdfast trust"
	}
	return p, ""
}

// PolicyText returns the policy file as written, and whether it is approved.
func (d *Dir) PolicyText() (text string, approved bool, err error) {
	data, err := os.ReadFile(filepath.Join(d.path, PolicyFile))
	if errors.Is(err, fs.ErrNotExist) {
		return d.base.String(), true, nil
	}
	if err != nil {
		return "", false, err
	}
	if d.approved(data) {
		return string(data), true, nil
	}
	p, err := policy.Parse(string(data))
	return string(data), err == nil && d.grantsNothing(p), nil
}

// Unwidened reports whether the policy file is in force only because it says
// what the defaults say, without anyone having approved it on this machine.
func (d *Dir) Unwidened() bool {
	data, err := os.ReadFile(filepath.Join(d.path, PolicyFile))
	if err != nil || d.approved(data) {
		return false
	}
	p, err := policy.Parse(string(data))
	return err == nil && d.grantsNothing(p)
}

// grantsNothing reports whether a policy says exactly what the defaults say.
// Such a file is in force without anyone approving it, because approving it
// would change nothing: a fresh clone of a project that never widened its
// policy works at once, with no warning to read and nothing to run.
func (d *Dir) grantsNothing(p *policy.Policy) bool {
	return p.String() == d.base.String()
}

// SetPolicy writes the policy file and approves it. Only commands a person
// runs call this; nothing an agent can reach through the tool interface does.
func (d *Dir) SetPolicy(p *policy.Policy) error { return d.SetPolicyText(p.String()) }

// SetPolicyText writes the policy file as given, so a person's own comments
// and layout survive, and approves it. The text must be a valid policy.
func (d *Dir) SetPolicyText(text string) error {
	if _, err := policy.Parse(text); err != nil {
		return err
	}
	path := filepath.Join(d.path, PolicyFile)
	tmp, err := os.CreateTemp(d.path, PolicyFile+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return d.Trust()
}

// Trust approves the policy file as it is now, on this machine.
func (d *Dir) Trust() error {
	data, err := os.ReadFile(filepath.Join(d.path, PolicyFile))
	if err != nil {
		return err
	}
	if _, err := policy.Parse(string(data)); err != nil {
		return fmt.Errorf("the policy file has a mistake: %w", err)
	}
	file, err := approvalsFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(file+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	var keep []string
	old, _ := os.ReadFile(file)
	for _, line := range strings.Split(string(old), "\n") {
		if _, path, ok := strings.Cut(line, " "); ok && path != d.path {
			keep = append(keep, line)
		}
	}
	keep = append(keep, digest(data)+" "+d.path)
	tmp, err := os.CreateTemp(filepath.Dir(file), "approved.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(strings.Join(keep, "\n") + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

func (d *Dir) approved(data []byte) bool {
	file, err := approvalsFile()
	if err != nil {
		return false
	}
	list, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	want := digest(data) + " " + d.path
	for _, line := range strings.Split(string(list), "\n") {
		if line == want {
			return true
		}
	}
	return false
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// approvalsFile is where this machine keeps the list of policies a person has
// approved. It lives outside every project, so a repository cannot bring its
// own approval along.
func approvalsFile() (string, error) {
	if dir := os.Getenv("HOLDFAST_STATE"); dir != "" {
		return filepath.Join(dir, "approved"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "holdfast", "approved"), nil
}

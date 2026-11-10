// Package sim is a small pretend machine: files, hosts, ports, web pages and
// commands held in memory. The live demo and the tests prove facts against it
// with the same engine that proves them against a real machine.
package sim

import (
	"bytes"
	"context"
	"errors"
	"hash/fnv"
	"io"
	"net"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gallowforge/holdfast/internal/policy"
	"github.com/gallowforge/holdfast/internal/world"
)

// File is one file of the pretend machine.
type File struct {
	Data    []byte
	ModTime time.Time
}

// Page is what a pretend web address answers.
type Page struct {
	Status int
	Body   string
}

// Result is what a pretend command does.
type Result struct {
	Exit int
	Out  string
}

// World is the pretend machine. The zero value is an empty machine; fill the
// maps before use and change them through the methods afterwards.
type World struct {
	mu    sync.Mutex
	Clock func() time.Time
	Files map[string]*File        // by clean relative path
	Names map[string][]netip.Addr // host name → addresses
	Ports map[string]bool         // "host:port" → listening
	Pages map[string]Page         // "https://host/path" → answer
	Cmds  map[string]Result       // "program arg arg" → result
}

var _ world.World = (*World)(nil)
var _ world.Pacer = (*World)(nil)

// New returns an empty machine that tells the time with clock.
func New(clock func() time.Time) *World {
	return &World{
		Clock: clock,
		Files: map[string]*File{},
		Names: map[string][]netip.Addr{"localhost": {netip.MustParseAddr("127.0.0.1")}},
		Ports: map[string]bool{},
		Pages: map[string]Page{},
		Cmds:  map[string]Result{},
	}
}

func (w *World) Now() time.Time { return w.Clock() }

// Change runs fn with the machine locked, for edits made while checks may run.
func (w *World) Change(fn func(w *World)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fn(w)
}

// Write puts a file on the machine, modified at the given time.
func (w *World) Write(name, data string, mod time.Time) {
	w.Files[path.Clean(name)] = &File{Data: []byte(data), ModTime: mod}
}

// inside applies the policy's folders to a path, by its spelling alone: the
// pretend machine has no symbolic links.
func inside(roots []string, name string) (string, error) {
	clean := path.Clean(name)
	if !path.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, "../") {
		for _, r := range roots {
			r = path.Clean(r)
			if r == "." || clean == r || strings.HasPrefix(clean, r+"/") {
				return clean, nil
			}
		}
	}
	return "", policy.Outside(name, path.Dir(clean))
}

func (w *World) Stat(roots []string, name string) (world.FileInfo, error) {
	clean, err := inside(roots, name)
	if err != nil {
		return world.FileInfo{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if f, ok := w.Files[clean]; ok {
		return world.FileInfo{Exists: true, Regular: true, Size: int64(len(f.Data)), ModTime: f.ModTime}, nil
	}
	for name := range w.Files {
		if clean == "." || strings.HasPrefix(name, clean+"/") {
			return world.FileInfo{Exists: true, Dir: true}, nil
		}
	}
	return world.FileInfo{}, nil
}

func (w *World) Open(roots []string, name string) (io.ReadCloser, error) {
	clean, err := inside(roots, name)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	f, ok := w.Files[clean]
	if !ok {
		return nil, errors.New("open " + name + ": no such file")
	}
	return io.NopCloser(bytes.NewReader(f.Data)), nil
}

func (w *World) lookup(host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	name := strings.ToLower(host)
	// As on a real machine, localhost and the names under .localhost are this
	// machine itself and are not looked up by name: they all answer as
	// localhost does, with a loopback address.
	local := policy.LocalName(name)
	if local {
		name = "localhost"
	}
	ips, ok := w.Names[name]
	if !ok || len(ips) == 0 {
		if local {
			return policy.Loopback(), nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return ips, nil
}

func (w *World) Resolve(_ context.Context, name string) ([]netip.Addr, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ips, err := w.lookup(name)
	return append([]netip.Addr(nil), ips...), err
}

// connect is one pretend TCP connection: resolve, ask the guard, see whether
// anything listens.
func (w *World) connect(addr string, guard world.Guard) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ips, err := w.lookup(host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if err := guard(host, ip); err != nil {
			return err
		}
	}
	if !w.Ports[strings.ToLower(addr)] {
		return errors.New("dial tcp " + addr + ": connect: connection refused")
	}
	return nil
}

func (w *World) Dial(_ context.Context, addr string, guard world.Guard) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.connect(addr, guard)
}

func (w *World) Get(_ context.Context, raw string, guard world.Guard, maxBody int64) (int, []byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, nil, err
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.connect(net.JoinHostPort(u.Hostname(), port), guard); err != nil {
		return 0, nil, err
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	page, ok := w.Pages[u.Scheme+"://"+strings.ToLower(u.Host)+p]
	if !ok {
		page = Page{Status: 404, Body: "not found"}
	}
	body := []byte(page.Body)
	if int64(len(body)) > maxBody {
		body = body[:maxBody]
	}
	return page.Status, body, nil
}

func (w *World) Run(_ context.Context, argv []string, maxOut int) (int, []byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.Cmds[strings.Join(argv, " ")]
	if !ok {
		return -1, nil, errors.New(argv[0] + ": executable file not found")
	}
	out := []byte(r.Out)
	if len(out) > maxOut {
		out = out[:maxOut]
	}
	return r.Exit, out, nil
}

// Took gives every check a steady, believable duration: nothing here takes
// real time, and a ledger that says 0 ms everywhere would read as fake.
func (w *World) Took(kind, target string) time.Duration {
	h := fnv.New32a()
	h.Write([]byte(kind + " " + target))
	n := time.Duration(h.Sum32())
	switch kind {
	case "http":
		return (38 + n%140) * time.Millisecond
	case "dns":
		return (2 + n%11) * time.Millisecond
	case "port":
		return (1 + n%4) * time.Millisecond
	case "cmd", "says":
		return (6 + n%30) * time.Millisecond
	default:
		return n % 2 * time.Millisecond
	}
}

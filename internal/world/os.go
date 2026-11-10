//go:build !js

package world

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gallowforge/holdfast/internal/policy"
)

// OS is the real machine. Root is the project folder: relative paths in checks
// and in the policy are read from there, and commands run there.
type OS struct {
	Root      string
	UserAgent string
}

func (o *OS) Now() time.Time { return time.Now() }

// abs turns a path as written (relative, absolute or ~/...) into a clean
// absolute one, without touching the disk.
func (o *OS) abs(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(o.Root, p)
	}
	return filepath.Clean(p), nil
}

// within finds the allowed folder that holds path and opens it as an os.Root,
// which refuses to follow a symbolic link out of the folder.
func (o *OS) within(roots []string, path string) (*os.Root, string, error) {
	target, err := o.abs(path)
	if err != nil {
		return nil, "", err
	}
	for _, r := range roots {
		base, err := o.abs(r)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(base, target)
		if err != nil || leaves(rel) {
			continue
		}
		root, err := os.OpenRoot(base)
		if err != nil {
			continue // an allowed folder that does not exist allows nothing
		}
		return root, rel, nil
	}
	return nil, "", policy.Outside(path, o.folder(path, target))
}

// folder names the folder a person would allow to let a check read path: the
// folder that holds it, and no more. It is written the way a policy file
// writes folders: from the project root where it is inside the root, from ~
// where it is under the home folder, and in full otherwise. Nothing on disk is
// looked at, so a refusal tells nothing about a path the policy does not allow.
func (o *OS) folder(path, target string) string {
	dir := filepath.Dir(target)
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	// A check of the root or of home itself needs that folder, not its parent.
	if target == filepath.Clean(o.Root) || target == home {
		dir = target
	}
	// A path written from the root is answered from the root even where it
	// climbs out of it (../shared), so the refusal does not tell where on the
	// disk the project lies.
	fromRoot := !filepath.IsAbs(path) && path != "~" && !strings.HasPrefix(path, "~/")
	if rel, err := filepath.Rel(o.Root, dir); err == nil && (fromRoot || !leaves(rel)) {
		return rel
	}
	if home != "" {
		if rel, err := filepath.Rel(home, dir); err == nil && !leaves(rel) {
			return filepath.Join("~", rel)
		}
	}
	return dir
}

// leaves reports whether a relative path climbs out of where it starts.
func leaves(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (o *OS) Stat(roots []string, path string) (FileInfo, error) {
	root, rel, err := o.within(roots, path)
	if err != nil {
		return FileInfo{}, err
	}
	defer root.Close()
	st, err := root.Stat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return FileInfo{}, nil
	}
	if err != nil {
		return FileInfo{}, escape(path, err)
	}
	return FileInfo{Exists: true, Dir: st.IsDir(), Regular: st.Mode().IsRegular(), Size: st.Size(), ModTime: st.ModTime()}, nil
}

func (o *OS) Open(roots []string, path string) (io.ReadCloser, error) {
	root, rel, err := o.within(roots, path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// Never open something that is not a plain file: a FIFO would block and a
	// device would never end.
	st, err := root.Stat(rel)
	if err != nil {
		return nil, escape(path, err)
	}
	if !st.Mode().IsRegular() {
		return nil, &policy.Blocked{Why: path + " is not a regular file"}
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, escape(path, err)
	}
	if st2, err := f.Stat(); err != nil || !st2.Mode().IsRegular() {
		f.Close()
		return nil, &policy.Blocked{Why: path + " is not a regular file"}
	}
	return f, nil
}

// escape turns "the path leaves its folder" into a policy answer and leaves
// ordinary errors (not found, permission) as they are.
func escape(path string, err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		return err
	}
	var pe *fs.PathError
	if errors.As(err, &pe) && strings.Contains(pe.Err.Error(), "escapes") {
		return &policy.Blocked{Why: path + " leads outside the folders the policy allows (through a symbolic link)"}
	}
	return err
}

// lookup asks the system's resolver for the addresses of a name. It is a
// variable so that a test can prove a name never reached it.
var lookup = net.DefaultResolver.LookupNetIP

// resolve finds the addresses of a host name. localhost and the names under
// .localhost are answered here with the loopback addresses and are never
// looked up: a resolver may pass such a name on to a DNS server, and the
// policy allowed it only as this machine.
func resolve(ctx context.Context, name string) ([]netip.Addr, error) {
	if policy.LocalName(name) {
		return policy.Loopback(), nil
	}
	return lookup(ctx, "ip", name)
}

// dial resolves host itself, asks the guard about each address and connects
// only to an address the guard allowed, so the answer cannot change between
// the check and the connection.
func dial(ctx context.Context, guard Guard, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else if ips, err = resolve(ctx, host); err != nil {
		return nil, err
	}
	var d net.Dialer
	var last error
	for _, ip := range ips {
		ip = ip.Unmap()
		if err := guard(host, ip); err != nil {
			if last == nil {
				last = err
			}
			continue
		}
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("%s has no addresses", host)
	}
	return nil, last
}

func (o *OS) Dial(ctx context.Context, addr string, guard Guard) error {
	c, err := dial(ctx, guard, "tcp", addr)
	if err != nil {
		return err
	}
	return c.Close()
}

func (o *OS) Resolve(ctx context.Context, name string) ([]netip.Addr, error) {
	ips, err := resolve(ctx, name)
	for i := range ips {
		ips[i] = ips[i].Unmap()
	}
	return ips, err
}

const maxRedirects = 5

func (o *OS) Get(ctx context.Context, raw string, guard Guard, maxBody int64) (int, []byte, error) {
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: nil, // a proxy would hide the real destination from the guard
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dial(ctx, guard, network, addr)
			},
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("more than %d redirects", maxRedirects)
			}
			if req.URL.User != nil {
				return errors.New("a redirect carried credentials")
			}
			return nil // every hop is dialled through the guard
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", o.UserAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	if err != nil {
		// The client wraps the reason in the address it was on, and after a
		// redirect that is whatever the server sent, password and query
		// string included. Keep the reason; the caller knows what it asked for.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, nil, err
	}
	defer resp.Body.Close()
	var body []byte
	if maxBody > 0 {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	}
	return resp.StatusCode, body, nil
}

// capped keeps the first max bytes written to it and quietly drops the rest.
type capped struct {
	buf []byte
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - len(c.buf); room > 0 {
		c.buf = append(c.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (o *OS) Run(ctx context.Context, argv []string, maxOut int) (int, []byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = o.Root
	// The command sees a plain, small environment: nothing of the caller's
	// tokens or settings, and messages in one language so "says" checks are stable.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C", "LC_ALL=C"}
	out := &capped{max: maxOut}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 2 * time.Second
	ownGroup(cmd) // so a timeout also stops whatever the command started
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, out.buf, nil
	case ctx.Err() != nil:
		return -1, out.buf, ctx.Err()
	case errors.Is(err, exec.ErrWaitDelay):
		// The command itself exited 0. Something it left running still held
		// its output open, and the wait for that was given up.
		return 0, out.buf, nil
	case errors.As(err, &ee):
		return ee.ExitCode(), out.buf, nil
	default:
		return -1, out.buf, err
	}
}

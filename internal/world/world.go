// Package world is everything a check can look at: files, the network and
// allowed commands. The real machine and the simulated one behind the live
// demo both implement it, so the same engine proves facts against either.
package world

import (
	"context"
	"io"
	"net/netip"
	"time"
)

// FileInfo is what a check may learn about a path without reading it.
type FileInfo struct {
	Exists  bool
	Dir     bool
	Regular bool
	Size    int64
	ModTime time.Time
}

// Guard is asked before every connection, with the address really being
// dialled. A non-nil error stops the connection.
type Guard func(host string, ip netip.Addr) error

// World is the machine as checks see it.
//
// File methods take the folders the policy allows; a path outside them, or one
// that leaves them through a symbolic link, gets a *policy.Blocked error.
// Network methods ask the guard about every address before connecting to it.
type World interface {
	Now() time.Time
	Stat(roots []string, path string) (FileInfo, error)
	Open(roots []string, path string) (io.ReadCloser, error)
	// Get makes one GET request and returns the status and at most maxBody
	// bytes of the body.
	Get(ctx context.Context, url string, guard Guard, maxBody int64) (status int, body []byte, err error)
	// Dial opens and closes one TCP connection.
	Dial(ctx context.Context, addr string, guard Guard) error
	Resolve(ctx context.Context, name string) ([]netip.Addr, error)
	// Run executes argv without a shell and returns the exit code and at most
	// maxOut bytes of what it printed.
	Run(ctx context.Context, argv []string, maxOut int) (exit int, out []byte, err error)
}

// Pacer is implemented by a simulated world to say how long a check "took",
// since nothing in it takes real time.
type Pacer interface {
	Took(kind, target string) time.Duration
}

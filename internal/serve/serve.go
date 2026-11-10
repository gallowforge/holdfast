// Package serve puts the ledger in a browser: the built interface and the
// API behind it, on this machine only.
//
// The page can retract facts and vouch for them, so the server is careful
// about who is talking to it. It listens on the loopback address unless told
// otherwise, answers only requests addressed to it by a local name (which
// stops another site from reaching it through a DNS trick), and takes API
// calls only with a header a page on another site cannot send.
package serve

import (
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gallowforge/holdfast/internal/api"
)

// Header must be present on every API call. A browser will not let another
// site's page set it without asking this server first, and this server never
// says yes.
const Header = "X-Holdfast"

const maxBody = 64 << 10

// Handler serves the interface in files and the API behind it. When open is
// false, only requests addressed to a loopback name are answered.
func Handler(a *api.API, files fs.FS, open bool) http.Handler {
	static := http.FileServerFS(files)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/{method}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get(Header) == "" || !sameOrigin(r) {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(api.Reply{Error: "this call did not come from the Holdfast page"})
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			json.NewEncoder(w).Encode(api.Reply{Error: "the request is too large"})
			return
		}
		reply, status := a.Do(r.Context(), r.PathValue("method"), body)
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(reply)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(files, "index.html"); err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, "This holdfast binary was built without its web interface. Build it with \"make build\".\n")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		static.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		if !open && !local(r.Host) {
			http.Error(w, "holdfast answers on this machine only", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// local reports whether a Host header names this machine.
func local(host string) bool {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	// Names are the same name in any case: LOCALHOST is this machine too.
	name = strings.ToLower(strings.Trim(name, "[]"))
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

// sameOrigin reports whether a request comes from the page this server
// served. Browsers send Origin on every POST; a request without one comes
// from a tool, and the header check covers that.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

// Server returns an HTTP server with sensible limits.
func Server(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// Proving every fact may take a while; see engine.VerifyBudget.
		WriteTimeout:   2 * time.Minute,
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: 16 << 10,
	}
}

// Loopback reports whether addr (host:port) binds to this machine only.
func Loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

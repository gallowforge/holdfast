package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// These tests hold for a binary built with the interface and for one built
// without it: they promise relations, not the names a build gives its files.

func TestBuiltSaysWhetherThePageIsThere(t *testing.T) {
	_, err := fs.Stat(Files(), "index.html")
	if Built() != (err == nil) {
		t.Errorf("Built() = %v, but looking for index.html says %v", Built(), err)
	}
	if Built() != Built() {
		t.Error("Built() changed its answer between two calls")
	}
}

func TestFilesStartAtThePageNotAtTheFolderItWasBuiltIn(t *testing.T) {
	files := Files()
	if files == nil {
		t.Fatal("Files() is nil")
	}
	// The root is always there, even in a binary without the interface.
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		t.Fatalf("the files cannot be listed: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "dist" {
			t.Error(`the files still have the folder "dist" in front of them`)
		}
	}
	if _, err := fs.Stat(files, "dist/index.html"); err == nil {
		t.Error("dist/index.html can be read: the page would be served at /dist/ and not at /")
	}
	// Nothing outside the built folder can be reached.
	for _, name := range []string{"../web.go", "web.go", "/index.html", "../dist/index.html"} {
		if _, err := fs.Stat(files, name); err == nil {
			t.Errorf("%q can be read through Files()", name)
		}
	}
	if !Built() {
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") {
				t.Errorf("a binary without the interface still carries %q", e.Name())
			}
		}
	}
}

var (
	linked = regexp.MustCompile(`(?:src|href)="([^"]*)"`)
	script = regexp.MustCompile(`(?s)<script\b([^>]*)>(.*?)</script>`)
)

// The server sends a content policy that allows only its own files and no
// inline script, so a page that needs anything else would stay blank.
func TestThePageNeedsOnlyFilesThatAreBuiltIn(t *testing.T) {
	if !Built() {
		t.Skip(`this build has no web interface (run "make build" to have one); there is no page to look at`)
	}
	files := Files()
	raw, err := fs.ReadFile(files, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	if !strings.Contains(strings.ToLower(page), "<!doctype html>") {
		t.Error("index.html does not begin as an HTML page")
	}

	links := linked.FindAllStringSubmatch(page, -1)
	if len(links) == 0 {
		t.Fatal("index.html names no script and no stylesheet: a page that does nothing")
	}
	for _, m := range links {
		link := m[1]
		if !strings.HasPrefix(link, "/") || strings.HasPrefix(link, "//") {
			t.Errorf("index.html asks for %q: only files of this server, named from its root, are allowed", link)
			continue
		}
		info, err := fs.Stat(files, strings.TrimPrefix(link, "/"))
		if err != nil {
			t.Errorf("index.html asks for %q, which is not built in: %v", link, err)
			continue
		}
		if info.IsDir() || info.Size() == 0 {
			t.Errorf("index.html asks for %q, which is empty or a folder", link)
		}
	}

	for _, m := range script.FindAllStringSubmatch(page, -1) {
		if !strings.Contains(m[1], "src=") || strings.TrimSpace(m[2]) != "" {
			t.Errorf("index.html has a script written into the page, which the content policy stops: <script%s>", m[1])
		}
	}
	// A script in an attribute is a script written into the page too.
	for _, banned := range []string{"onload=", "onclick=", "onerror=", "javascript:"} {
		if strings.Contains(strings.ToLower(page), banned) {
			t.Errorf("index.html contains %q, which the content policy stops", banned)
		}
	}
}

func TestEveryBuiltFileCanBeRead(t *testing.T) {
	seen := 0
	err := fs.WalkDir(Files(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		seen++
		_, err = fs.ReadFile(Files(), name)
		return err
	})
	if err != nil {
		t.Errorf("walking the built files: %v", err)
	}
	if Built() && seen < 2 {
		t.Errorf("the interface is %d file: a page without its script", seen)
	}
}

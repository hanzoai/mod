package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, c := range []struct {
		path string
		want ask
		ok   bool
	}{
		{"/github.com/hanzoai/o11y/@v/list", ask{"github.com/hanzoai/o11y", list, ""}, true},
		{"/github.com/hanzoai/o11y/@latest", ask{"github.com/hanzoai/o11y", latest, ""}, true},
		{"/github.com/hanzoai/o11y/@v/v1.5.64.info", ask{"github.com/hanzoai/o11y", info, "v1.5.64"}, true},
		{"/github.com/hanzoai/o11y/@v/v1.5.64.mod", ask{"github.com/hanzoai/o11y", gomod, "v1.5.64"}, true},
		{"/github.com/hanzoai/o11y/@v/v1.5.64.zip", ask{"github.com/hanzoai/o11y", zip, "v1.5.64"}, true},
		// Case escaping: the wire spells an upper-case letter "!"+lower. Every
		// decision downstream is made on the unescaped path.
		{"/github.com/!burnt!sushi/toml/@v/list", ask{"github.com/BurntSushi/toml", list, ""}, true},
		{"/github.com/hanzoai/o11y/@v/v1.5.64.tar", ask{}, false},
		{"/github.com/hanzoai/o11y", ask{}, false},
		{"/", ask{}, false},
	} {
		got, ok := parse(c.path)
		if ok != c.ok || got != c.want {
			t.Errorf("parse(%q) = %+v,%v want %+v,%v", c.path, got, ok, c.want, c.ok)
		}
	}
}

func TestOurs(t *testing.T) {
	p := &Proxy{Namespaces: []string{"github.com/hanzoai", "github.com/luxfi"}}
	for _, c := range []struct {
		mod  string
		want bool
	}{
		{"github.com/hanzoai/o11y", true},
		{"github.com/hanzoai/kms/sdk/go", true},
		{"github.com/luxfi/node", true},
		{"github.com/google/uuid", false},
		// A namespace is a path prefix, not a string prefix. Were it the latter,
		// anyone who can register `hanzoaiX` chooses which forge we read from.
		{"github.com/hanzoaiX/evil", false},
		{"github.com/hanzoai-evil/x", false},
		{"evil.example/github.com/hanzoai/x", false},
	} {
		if got := p.ours(c.mod); got != c.want {
			t.Errorf("ours(%q) = %v want %v", c.mod, got, c.want)
		}
	}
}

// newProxy builds a proxy whose forge cannot be reached, so every resolution
// inside its namespaces fails. That is the interesting case: what a failure
// tells the go command to do next.
func newProxy(t *testing.T) *Proxy {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "scratch")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Proxy{
		Namespaces: []string{"github.com/hanzoai"},
		Forge:      "https://forge.invalid",
		Upstream:   "https://upstream.invalid",
		Token:      "SENTINEL-TOKEN-VALUE",
		Dir:        dir,
		Cache:      filepath.Join(root, "cache"),
	}
}

// A public module we cannot resolve gets 404, this protocol's "ask the next
// proxy". There is nothing in a public path to protect.
func TestPublicFallsThrough(t *testing.T) {
	w := httptest.NewRecorder()
	newProxy(t).ServeHTTP(w, httptest.NewRequest("GET", "/github.com/google/uuid/@v/list", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d want %d", w.Code, http.StatusNotFound)
	}
}

// Where a module's bytes come from is decided by the namespace and nothing
// else: ours from the forge, everyone else's from the public proxy.
func TestSourceFollowsNamespace(t *testing.T) {
	p := newProxy(t)
	if !has(p.env(true), "GOPROXY=direct") {
		t.Error("our modules must resolve from the forge, not through a proxy")
	}
	if !has(p.env(false), "GOPROXY="+p.Upstream) {
		t.Error("third-party modules must resolve from the public proxy")
	}
}

// The disclosure boundary. A module inside our namespaces that we could not
// resolve must NOT answer 404: 404 sends the go command to the next proxy
// carrying the module path, and the next proxy is public. The path of an
// unreleased repository is exactly what must not travel.
func TestPrivateFailureNeverFallsThrough(t *testing.T) {
	for _, path := range []string{
		"/github.com/hanzoai/unreleased/@v/list",
		"/github.com/hanzoai/unreleased/@latest",
		"/github.com/hanzoai/unreleased/@v/v1.0.0.info",
		"/github.com/hanzoai/unreleased/@v/v1.0.0.mod",
		"/github.com/hanzoai/unreleased/@v/v1.0.0.zip",
	} {
		w := httptest.NewRecorder()
		newProxy(t).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code == http.StatusNotFound || w.Code == http.StatusGone {
			t.Errorf("%s answered %d — the go command would carry this path to the public proxy", path, w.Code)
		}
		if w.Code != http.StatusBadGateway {
			t.Errorf("%s = %d want %d", path, w.Code, http.StatusBadGateway)
		}
	}
}

// Whatever git says on the way out, it does not say the token. git quotes the
// URL it was handed in most of its errors, and that URL carries the credential.
func TestErrorsCarryNoCredential(t *testing.T) {
	p := newProxy(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", "/github.com/hanzoai/unreleased/@v/list", nil))
	if strings.Contains(w.Body.String(), p.Token) {
		t.Fatalf("response carries the credential: %s", w.Body.String())
	}
}

// The credential reaches git through the environment of the child and nothing
// else, so it never lands in a file, a layer, or another process on the box.
func TestCredentialTravelsInTheEnvironment(t *testing.T) {
	p := newProxy(t)
	env := p.env(true)
	var rule, count string
	for _, e := range env {
		if k, v, _ := strings.Cut(e, "="); k == "GIT_CONFIG_KEY_0" {
			rule = v
		} else if k == "GIT_CONFIG_COUNT" {
			count = v
		}
	}
	if count != "1" {
		t.Fatalf("GIT_CONFIG_COUNT = %q want 1", count)
	}
	want := "url.https://x:" + p.Token + "@forge.invalid/hanzoai/.insteadOf"
	if rule != want {
		t.Fatalf("rewrite rule = %q want %q", rule, want)
	}
	// GOPRIVATE keeps the public checksum database out of the resolution of a
	// module it cannot have seen. go.sum still pins every one of them.
	if !has(env, "GOPRIVATE=github.com/hanzoai") || !has(env, "GONOSUMDB=github.com/hanzoai") {
		t.Fatalf("private namespaces not exempted from the public sum database: %v", env)
	}
}

func has(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

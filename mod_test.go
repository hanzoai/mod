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

// A module we are not authoritative for gets 404, this protocol's "ask the next
// proxy". Continuing the walk is the right answer for a name we do not answer
// for.
func TestOtherPeoplesModulesFallThrough(t *testing.T) {
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

// The authority boundary, and the single most important behaviour here. A
// module inside our namespaces that we could not resolve must NOT answer 404.
//
// 404 means "ask the next proxy", and the next proxy resolves
// github.com/hanzoai/x from github.com — so a 404 does not fail the build, it
// quietly sends it back to the host this service exists so we need not depend
// on, and the build goes green having used it. This holds whether or not the
// module is public: it is about which source answered, not who may read it.
func TestOurFailuresNeverFallThrough(t *testing.T) {
	for _, path := range []string{
		"/github.com/hanzoai/absent/@v/list",
		"/github.com/hanzoai/absent/@latest",
		"/github.com/hanzoai/absent/@v/v1.0.0.info",
		"/github.com/hanzoai/absent/@v/v1.0.0.mod",
		"/github.com/hanzoai/absent/@v/v1.0.0.zip",
	} {
		w := httptest.NewRecorder()
		newProxy(t).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code == http.StatusNotFound || w.Code == http.StatusGone {
			t.Errorf("%s answered %d — the go command would resolve this from github.com and call it a success", path, w.Code)
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
	p.ServeHTTP(w, httptest.NewRequest("GET", "/github.com/hanzoai/absent/@v/list", nil))
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
}

// Reaching the forge and being allowed to read a given repository there are
// different questions. With no credential the rewrite still happens — otherwise
// the name resolves from github.com, which is the thing this must never do.
func TestNoCredentialStillPointsAtTheForge(t *testing.T) {
	p := newProxy(t)
	p.Token = ""
	if got, want := p.dial(), "https://forge.invalid"; got != want {
		t.Fatalf("dial = %q want %q", got, want)
	}
	if !has(p.env(true), "GIT_CONFIG_KEY_0=url.https://forge.invalid/hanzoai/.insteadOf") {
		t.Fatalf("no credential dropped the rewrite, so our names would resolve from github.com: %v", p.env(true))
	}
}

// The public transparency log is asked about everything by default, ours
// included — it is the only check that sees the forge and the published record
// of the same name together. Unlogged is an exception list, and an empty one is
// the resting state.
func TestEverythingVerifiesAgainstThePublicLog(t *testing.T) {
	p := newProxy(t)
	if !has(p.env(true), "GONOSUMDB=") {
		t.Errorf("our modules were exempted from the public log with nothing declared unlogged: %v", p.env(true))
	}
	p.Unlogged = []string{"github.com/hanzoai/sealed"}
	if !has(p.env(true), "GONOSUMDB=github.com/hanzoai/sealed") {
		t.Errorf("declared unlogged path not exempted: %v", p.env(true))
	}
	// GOPRIVATE means "bypass the proxy AND the log". Bypassing the proxy is
	// what reaches github.com, so this name must not appear here or in any
	// client's environment.
	for _, e := range p.env(true) {
		if strings.HasPrefix(e, "GOPRIVATE=") {
			t.Errorf("GOPRIVATE is set (%q) — it bypasses the proxy, which is the one thing that must not happen", e)
		}
	}
}

// An unset list is no paths. Splitting "" yields one empty string, and an empty
// path is a prefix of every module — the list's meaning exactly inverted.
func TestEmptyListIsNoPaths(t *testing.T) {
	if got := paths(""); len(got) != 0 {
		t.Errorf(`paths("") = %v want empty`, got)
	}
	got := paths("github.com/hanzoai/, , github.com/luxfi")
	if len(got) != 2 || got[0] != "github.com/hanzoai" || got[1] != "github.com/luxfi" {
		t.Errorf("paths = %v want [github.com/hanzoai github.com/luxfi]", got)
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

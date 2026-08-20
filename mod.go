// Package main serves the Go module proxy protocol for modules whose source
// lives on our forge.
//
// This exists for retention and authority. It is not a privacy mechanism, and
// nothing here should be read as one: whether a module is public changes who
// may read it, not who answers for its name or how long it stays reachable.
//
// AUTHORITY. A module path is identity, not an address: `module
// github.com/hanzoai/o11y` is the name every dependent's go.mod writes down,
// and renaming it would rewrite every dependent. The source, meanwhile, lives
// on git.hanzo.ai. A proxy is where those two facts meet — it answers for the
// name and reads from the forge, so a build resolves the module it already
// names without dialing the host in the name. The name still says github.com;
// the answer no longer has to come from there.
//
// RETENTION. It answers for every module, not only ours. A module in one of our
// namespaces resolves from the forge; anything else resolves from the public
// proxy. Both land in one module cache and stay there, which is the difference
// between depending on a copy someone else keeps and holding our own.
// github.com/hanzoai/s3-go is the proof: it is a dependency of the forge's own
// build, its GitHub repository no longer exists, and builds kept working only
// because a cache elsewhere happened to still hold it. The forge holds the
// source and its tags, so here it resolves because we decided it does.
//
// The guarantee has a shape worth stating, and the two halves are not equal.
// For our namespaces it is total, because the forge is the source rather than a
// copy of one. For a third-party module it covers WHAT WE HAVE ALREADY SERVED —
// this cache does not evict, so a module that came through once remains
// available — and nothing we have never fetched. A dependency added tomorrow
// from an origin that vanishes tonight is not covered by anything here.
//
// Protocol: https://go.dev/ref/mod#goproxy-protocol
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"

	"golang.org/x/mod/module"
)

// Proxy answers for a set of module namespaces, resolving each from the forge.
type Proxy struct {
	// Namespaces are the module path prefixes this proxy is authoritative for,
	// each of the form `<host>/<org>`. A module under one of them resolves from
	// the forge; anything else is somebody else's to answer.
	Namespaces []string
	// Forge is the base URL that holds the source, e.g. https://git.hanzo.ai.
	Forge string
	// Upstream resolves everything outside our namespaces.
	Upstream string
	// Unlogged are the module paths the public checksum log cannot vouch for,
	// because it has never seen them. Everything else — ours included — is
	// checked against that log, so this list is empty until something needs to
	// be in it, and shrinks back to empty as its entries become published.
	Unlogged []string
	// Token authenticates to the forge, and is only needed for source the forge
	// does not serve anonymously. It is passed to git through the environment of
	// each child process and is never written to a file. Empty is a working
	// configuration.
	Token string
	// Dir is the scratch module the toolchain resolves in, and the HOME those
	// children see.
	Dir string
	// Cache is GOMODCACHE, where resolved modules are kept. Warm, this is the
	// whole cost of a request: a file read. Nothing prunes it — retention is the
	// point, so give it durable storage.
	Cache string
}

// verbs are the five requests the protocol defines.
const (
	list   = "list"
	latest = "latest"
	info   = "info"
	gomod  = "mod"
	zip    = "zip"
)

// ask is one parsed protocol request.
type ask struct {
	module  string // unescaped module path
	verb    string
	version string // unescaped; empty for list and latest
}

// parse reads a request path. Module paths and versions arrive case-escaped
// (an upper-case letter as "!" + lower), so they are unescaped here and every
// decision below is made on the real path — the alternative is a namespace
// check that `GitHub.com/HanzoAI/x` walks straight past.
func parse(p string) (ask, bool) {
	p = strings.TrimPrefix(p, "/")
	if raw, ok := strings.CutSuffix(p, "/@latest"); ok {
		m, err := module.UnescapePath(raw)
		if err != nil {
			return ask{}, false
		}
		return ask{module: m, verb: latest}, true
	}
	i := strings.Index(p, "/@v/")
	if i < 0 {
		return ask{}, false
	}
	m, err := module.UnescapePath(p[:i])
	if err != nil {
		return ask{}, false
	}
	rest := p[i+len("/@v/"):]
	if rest == list {
		return ask{module: m, verb: list}, true
	}
	ext := path.Ext(rest)
	switch ext {
	case ".info", ".mod", ".zip":
	default:
		return ask{}, false
	}
	v, err := module.UnescapeVersion(strings.TrimSuffix(rest, ext))
	if err != nil {
		return ask{}, false
	}
	return ask{module: m, verb: ext[1:], version: v}, true
}

// ours reports whether a module path falls in one of our namespaces.
//
// The trailing separator is what makes this a namespace test rather than a
// string test: without it `github.com/hanzoaiX/evil` is inside
// `github.com/hanzoai`, and a stranger picks the namespace they resolve from.
func (p *Proxy) ours(mod string) bool {
	for _, ns := range p.Namespaces {
		if mod == ns || strings.HasPrefix(mod, ns+"/") {
			return true
		}
	}
	return false
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := parse(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ours := p.ours(req.module)
	err := p.answer(w, req, ours)
	if err == nil {
		return
	}
	if !ours {
		// Somebody else's module we could not resolve. 404 is this protocol's
		// "ask the next proxy", and for a module we are not authoritative for
		// that is the right answer — let the walk continue.
		http.NotFound(w, r)
		return
	}
	// Inside our namespaces 404 is the one answer that must never be given, and
	// the reason has nothing to do with whether the module is public.
	//
	// 404 means "ask the next proxy". The next proxy resolves
	// github.com/hanzoai/x the way its name reads — from github.com. So a 404
	// here does not fail; it silently hands resolution back to the exact host
	// this service exists so we need not depend on, and the build goes GREEN
	// having used it. A false success is worse than a failure, because nobody
	// looks at it.
	//
	// Any other status ends the walk. If the forge cannot answer for a name we
	// are authoritative for, that is a fact the build should hear.
	http.Error(w, "resolve "+req.module+": "+err.Error(), http.StatusBadGateway)
}

func (p *Proxy) answer(w http.ResponseWriter, req ask, ours bool) error {
	if req.verb == list {
		out, err := p.run(ours, "list", "-m", "-versions", req.module)
		if err != nil {
			return err
		}
		// `go list -m -versions` prints the path then its versions, space
		// separated. The protocol wants one version per line and no path.
		fields := strings.Fields(string(out))
		if len(fields) == 0 {
			return errors.New("no version list for " + req.module)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for _, v := range fields[1:] {
			fmt.Fprintln(w, v)
		}
		return nil
	}

	version := req.version
	if req.verb == latest {
		version = latest
	}
	out, err := p.run(ours, "mod", "download", "-json", req.module+"@"+version)
	if err != nil {
		return err
	}
	var got struct {
		Info, GoMod, Zip, Error string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		return err
	}
	if got.Error != "" {
		return errors.New(scrub(got.Error, p.Token))
	}

	// The toolchain wrote the three artifacts; serving them is a file read. They
	// are byte-identical to what a direct fetch produces, which is what lets the
	// caller's go.sum verify them unchanged.
	file, mime := got.Info, "application/json"
	switch req.verb {
	case gomod:
		file, mime = got.GoMod, "text/plain; charset=utf-8"
	case zip:
		file, mime = got.Zip, "application/zip"
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	w.Header().Set("Content-Type", mime)
	_, err = io.Copy(w, f)
	return err
}

// run invokes the toolchain in the scratch module.
//
// Resolution is the toolchain's own, not a second implementation of it: module
// zips have rules about nested modules, vendor directories, symlinks, file
// modes and case collisions, and a zip that gets any of them wrong hashes
// differently and fails every caller's go.sum. The one correct implementation
// of that is the one shipped with Go.
func (p *Proxy) run(ours bool, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = p.Dir
	cmd.Env = p.env(ours)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil && stdout.Len() == 0 {
		return nil, errors.New(scrub(strings.TrimSpace(stderr.String()), p.Token))
	}
	return stdout.Bytes(), nil
}

func (p *Proxy) env(ours bool) []string {
	// Where the bytes come from is the one thing that differs between a module
	// of ours and anyone else's: ours has a forge holding its source, and git
	// reaches it directly. Everything else has a public proxy.
	from := p.Upstream
	if ours {
		from = "direct"
	}
	env := []string{
		"HOME=" + p.Dir,
		"PATH=" + os.Getenv("PATH"),
		"GOMODCACHE=" + p.Cache,
		"GOFLAGS=-mod=mod",
		"GOPROXY=" + from,
		// Everything is checked against the public transparency log, ours
		// included. That check is worth MORE for our modules than for anyone
		// else's: this is the one place that reads the forge while the log
		// remembers what was published under the same name, so bytes that
		// disagree are caught here and nowhere else.
		//
		// Unlogged is the exception list, and it is empty by default. A module
		// the log has never seen cannot be checked against it, and asking fails
		// the fetch — so an entry here says "the log has nothing to say about
		// this name", not "do not verify it". The caller's go.sum still pins it
		// either way.
		"GONOSUMDB=" + strings.Join(p.Unlogged, ","),
		// Resolve with the toolchain in the image. Fetching another one would
		// need the network we are the network for.
		"GOTOOLCHAIN=local",
	}
	// Point git at the forge for the length of this process. This rewrite IS the
	// authority: it is what makes a name that reads github.com resolve from the
	// forge. Config in the environment rather than a file keeps the credential
	// out of the image, out of any layer, and out of every other process on the
	// box.
	var i int
	for _, n := range p.Namespaces {
		org := n[strings.LastIndex(n, "/")+1:]
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=url.%s/%s/.insteadOf", i, p.dial(), org),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=https://%s/", i, n),
		)
		i++
	}
	env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", i), "GIT_TERMINAL_PROMPT=0")
	return env
}

// dial is the forge URL git is pointed at, carrying the credential when there is
// one. The rewrite happens either way — reaching the forge is not the same
// question as being allowed to read a particular repository there, and source
// the forge serves anonymously needs no credential to reach.
func (p *Proxy) dial() string {
	if p.Token == "" {
		return p.Forge
	}
	host, _ := strings.CutPrefix(p.Forge, "https://")
	return "https://x:" + p.Token + "@" + host
}

// scrub removes the credential from text on its way to a log or a response.
// git quotes the URL it was given in most of its errors, and that URL carries
// the token.
func scrub(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "…")
}

package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Configuration is read from the environment because every value here is
// deployment wiring — where the forge is, which namespaces we answer for — and
// none of it is a runtime policy anyone edits.
func main() {
	forge := env("FORGE", "https://git.hanzo.ai")
	token := os.Getenv("FORGE_TOKEN")
	addr := env("ADDR", ":8080")
	root := env("ROOT", "/var/lib/mod")

	names := paths(env("NAMESPACES", "github.com/hanzoai"))
	// Empty by default: the public checksum log is asked about everything unless
	// a name is listed here, and a name belongs here only while the log has
	// never seen it.
	unlogged := paths(os.Getenv("UNLOGGED"))

	dir := filepath.Join(root, "scratch")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatal(err)
	}
	// One scratch module gives the toolchain the module context its resolution
	// commands need. It requires nothing and builds nothing.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module scratch\n"), 0o600); err != nil {
		log.Fatal(err)
	}

	p := &Proxy{
		Namespaces: names,
		Forge:      strings.TrimSuffix(forge, "/"),
		Upstream:   env("UPSTREAM", "https://proxy.golang.org"),
		Unlogged:   unlogged,
		Token:      token,
		Dir:        dir,
		Cache:      filepath.Join(root, "cache"),
	}

	mux := http.NewServeMux()
	mux.Handle("/", p)
	// Liveness has to answer without touching the forge, or a forge blip
	// restarts a proxy that is working.
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})

	log.Printf("mod: serving %s from %s on %s", strings.Join(names, " "), p.Forge, addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// paths splits a comma-separated list of module paths. An empty string is an
// empty list, not a list holding one empty path — and an empty path would match
// every module, which is the whole list's meaning inverted.
func paths(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.Trim(strings.TrimSpace(p), "/"); p != "" {
			out = append(out, p)
		}
	}
	return out
}

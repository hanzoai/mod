# mod — working notes

The Go module proxy. Read `README.md` first for what it is and how it is
configured; this file holds the decisions behind it.

## What it is for, after the OSS decision

This was first built to keep a GitHub credential out of builds that needed
private modules. The estate is going fully open source, so that problem is
ending rather than being solved, and the justification narrows to the two
reasons that never depended on privacy:

- **Authority** — the forge answers for our module names, so a build does not
  depend on github.com being up, willing, or still holding the repository.
- **Retention** — one cache, nothing prunes it, so a module stays reachable
  because we decided it does.

`github.com/hanzoai/s3-go` is the whole argument in one line: 404 on GitHub,
alive on the forge, and our builds only kept working because a third-party cache
happened to still hold it.

Read anything below as serving those two. Nothing here is a privacy mechanism.

## Why a proxy rather than the alternatives

1. **A proxy we own** — this. The module path is answered by us and read from
   the forge. One change in the client and module identity is untouched.
2. **Migrate the module paths** to a git.hanzo.ai host. Cleanest in principle,
   and the blast radius is why it is not the answer: 190 module definitions, 124
   dependent `go.mod` files, ~21,500 importing `.go` files in the hanzo tree
   alone, plus 33 required paths not even checked out locally.
3. **A machine credential at GitHub.** Keeps the dependency, only changes whose
   name is on it. It does nothing for authority and nothing for retention.

The forge already had half of (1): `routers/api/packages/goproxy` in `hanzoai/git`
is a read-through cache of the public ecosystem and it is live. It cannot be the
build-time proxy, and the reason is worth keeping: it is a registry of what has
been PUBLISHED to it, with no way to resolve a module from the repository whose
source it is already holding. Different jobs.

## The 404 rule — the load-bearing one

In the GOPROXY protocol 404 means "ask the next proxy". The next proxy resolves
`github.com/hanzoai/x` the way its name reads: from github.com. So a 404 inside
our namespaces does not fail — it silently returns resolution to the host we
exist so we need not depend on, and **the build goes green having used it**. A
false success is worse than a failure because nobody looks at it.

`TestOurFailuresNeverFallThrough` holds this. Do not "fix" it to return 404. It
was written for a privacy reason that has since evaporated; the reason above is
the one that survives, and it is stronger.

## Checksums

Everything is checked against the public transparency log by default, ours
included, and that check is worth more for our modules than for anyone else's:
this proxy is the one place that reads the forge while the log remembers what
was published under the same name. A forge copy whose bytes disagree with the
published tag is caught here or not at all.

`UNLOGGED` is the exception list and it is EMPTY by default. A module the log has
never seen cannot be checked against it and asking fails the fetch, so an entry
says "the log has nothing to say about this name" — not "do not verify it". The
caller's `go.sum` still pins it either way. Entries leave the list as their
repositories are published; when it is empty for good, delete the variable.

`GOPRIVATE` is the wrong knob and reaching for it is the original bug. It means
"bypass the proxy AND the log", and bypassing the proxy is what sends the
toolchain to github.com. A test asserts the server never sets it.

## The forge credential is also a transition value

Public forge repositories are readable anonymously — verified, not assumed:

```sh
GIT_TERMINAL_PROMPT=0 git -c credential.helper= ls-remote https://git.hanzo.ai/hanzoai/ci.git
```

So `FORGE_TOKEN` is only needed for source the forge will not serve
anonymously, and empty is a working configuration. The URL rewrite happens with
or without it — reaching the forge and being allowed to read a given repository
there are different questions, and dropping the rewrite would send our names
back to github.com.

## Retention has a limit, and it is stated in the README

For our namespaces the guarantee is total, because the forge is the source
rather than a copy of one. For third-party modules it covers **what has already
been served and nothing never fetched**. Do not let it be written as more than
that.

Two modules are beyond saving by any proxy: `hanzoai/gh` (required by
`docdb/tools`) and `hanzoai/xfail` (required by `docdb/integration`) are absent
from github.com, from the public proxy, and from the forge. They need a source
before anything can serve them; the likely upstreams are `FerretDB/gh` and
`FerretDB/xfail`, both public.

## Build

go.mod says `go 1.26.5` and the Dockerfile pins `golang:1.26.5-bookworm`. This
is a binary — nothing imports it — so the directive constrains only its own
build, and naming the patch states the toolchain it is actually built and tested
with instead of a floor it happens to clear.

Separately: `gover` in `hanzoai/ci` read a two-part directive like `go 1.26` as
a demand for the newest patch of that minor, so it refused `golang:1.26.5` — an
image that builds it. Sixteen repos write a two-part directive, `mcp-gateway`
among them. Fixed on ci main (`6b1ea4f`) with three cases covering the floor;
that fix reaches the fleet when `v1` next moves, which is blocked on ci main's
`site` gate (four failures from `1177811`, unrelated to this).

The caller pins `@v1` and must keep doing so. This forge leaves
`GITHUB_WORKFLOW_REF` unset, so `build.yml` clones its own `bin/` tools from
`v1` no matter which ref the caller named — pinning an immutable tag here gets
that build.yml against `v1`'s tools, which is skew, not a canary.

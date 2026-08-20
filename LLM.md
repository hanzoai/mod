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

## The forge credential is structural, and going open source does not change it

The forge asks every reader to sign in, public repositories included:

```sh
curl -o /dev/null -w '%{http_code}\n' \
  https://git.hanzo.ai/hanzoai/ci.git/info/refs?service=git-upload-pack   # 401
```

So `FORGE_TOKEN` is required, and it stays required however much of the estate
is published. Publishing changes who may read a repository on GitHub; it does
not change that this forge authenticates.

An earlier note here claimed the opposite, from a `git -c credential.helper=`
check that quietly kept reading the system and global helpers. Verify anonymity
with `GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null`, or with plain
curl against `info/refs`, which has no helper to inherit.

The code still tolerates an empty token — it degrades to a plain rewrite rather
than embedding an empty credential in a URL — because the rewrite is what makes
our names resolve from the forge at all, and dropping it would send them back to
github.com. That is worth keeping as behaviour. It is not a deployment mode.

In the cluster the value comes from KMS at `hanzo:/deploy/FORGE_TOKEN`, synced
by the `git-hanzo-ai-token-kms-sync` KMSSecret into `git-hanzo-ai-token/token`.
It carries write scope and only needs read.

## Who actually needs this, measured

Of the 157 module versions our namespaces contribute across 22 Go repositories,
**155 are served by the public proxy and verified by the public log**. Two are
not, and they are the entire reason any repository cannot build without this
service:

| module | not on the public proxy | needed by |
|---|---|---|
| `github.com/hanzoai/thinking v0.1.1` | 404 | `zen`, `zen-gateway` |
| `github.com/hanzoai/voice` (pseudo-version) | 404 | `ai` |

Both resolve from the forge, and both are in `UNLOGGED` because the checksum log
has never seen them.

So **three** repositories need the proxy and **nineteen** carry a GitHub
credential for a module graph that is already entirely public: adnexus, base,
cloud, commerce, gateway, git, iam, ingress, kms, mcp-gateway, notify, o11y,
playground, s3-csi, superbase, team-go, visor, vm, world.

Two traps in measuring this again. `exclude` lines are not requires —
`exclude github.com/luxfi/genesis v1.5.21` appears in four go.mod files for a
version that exists nowhere, and reading it as a dependency invents four
repositories that need a proxy. And matching a repository name as a substring
makes `zen-gateway` answer for `gateway`.

`github.com/hanzoai/s3-go` deserves its own line: the public proxy DOES still
serve it, so nothing is failing today. Its GitHub repository is gone, so what
holds our builds up is a cache we do not own and cannot refill.

## Reachability: test gates yes, image builds no

Go modules are fetched at two moments and only one of them can reach this.

- **Test gates** run on the git-runner in the `hanzo` namespace. Proven: a pod
  there resolves through the proxy with no GitHub credential present.
- **Image builds** run in buildkitd in `hanzo-build`, and that namespace is
  labelled `hanzo.ai/component: build-isolated`. Traffic to `hanzo` times out to
  the Service AND to the pod IP, so it is neither DNS nor the Service.

The namespace policy admits `10.124.0.0/16` and `10.125.0.0/16` and every pod
involved sits in `10.125.0.0/16`, which looks like an allow and is not: Cilium
resolves an `ipBlock` against entities outside the cluster, and pod-to-pod
traffic carries an identity rather than an address. Naming the namespace is the
right shape and is what `mod`'s own policy now does — but it is NOT sufficient
here. `allow-build-to-s3` names `hanzo-build` the same way and `hanzo-build`
cannot reach `s3:9000` either, so the isolation is enforced below these
policies. Reaching it from a build is an owner decision about that boundary, not
a rule to add.

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

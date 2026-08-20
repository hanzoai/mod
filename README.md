# mod

The Go module proxy for the estate.

It exists for two reasons, and privacy is not one of them.

**Authority.** A module path is identity, not an address. `module
github.com/hanzoai/o11y` is the name every dependent's `go.mod` writes down, and
changing it rewrites every dependent. The source, meanwhile, lives on
git.hanzo.ai. This service is where those two facts meet: it answers for the
name and reads from the forge, so a build resolves the module it already names
without dialing the host in the name.

**Retention.** Everything it serves lands in one cache that nothing prunes.
`github.com/hanzoai/s3-go` is the case in point — it is required by `cloud`,
`gateway` and `amqp`, its GitHub repository no longer exists, and builds kept
working only because a cache somewhere else happened to still hold it. Whether a
module we depend on is still reachable should be our decision.

## Configuration

Clients need one variable:

```sh
GOPROXY=http://mod.hanzo.svc.cluster.local
```

No credential. No `GOPRIVATE` — that is shorthand for "bypass the proxy AND the
checksum log", and bypassing the proxy is exactly what sends the toolchain to
github.com with a credential in hand. Setting it is the original bug.

A module the public checksum log has never seen cannot be checked against it,
and asking fails the fetch. Those names — and only those — go in `GONOSUMDB`, on
the client and on the server. That list shrinks to nothing as the repositories
behind it are published, and then the variable goes away.

The server reads:

| variable | default | meaning |
|---|---|---|
| `FORGE` | `https://git.hanzo.ai` | where our source lives |
| `FORGE_TOKEN` | — | forge credential. Required: the forge asks every reader to sign in, public repositories included |
| `NAMESPACES` | `github.com/hanzoai` | module prefixes this proxy answers for |
| `UNLOGGED` | *(empty)* | paths the public checksum log has never seen |
| `UPSTREAM` | `https://proxy.golang.org` | where everything else resolves |
| `ROOT` | `/var/lib/mod` | scratch module and module cache |
| `ADDR` | `:8080` | listen address |

## What it guarantees

**Our namespaces.** Resolved from the forge, which holds the source and its
tags. GitHub is not consulted and no GitHub credential is involved. This is
total, not best-effort: it does not matter what happens to the GitHub
repository.

**Everything else.** Resolved from the public proxy and kept. Nothing prunes the
cache, so a module that came through once stays available if its origin
disappears. **This covers what we have already served and nothing we have never
fetched** — a dependency added tomorrow whose origin vanishes tonight is not
covered by anything here. The difference between holding our own copy and
trusting someone else's retention is real, but it is not a guarantee about the
whole ecosystem.

**A failure is a failure.** A module in our namespaces that cannot be resolved
answers 502, never 404. In the GOPROXY protocol 404 means "ask the next proxy",
and the next proxy resolves `github.com/hanzoai/…` from github.com — so a 404
here would not fail the build, it would silently hand resolution back to the
host this service exists so we need not depend on, and the build would go green
having used it. 502 ends the walk.

**Bytes are checked.** Every module resolved here is verified against the public
transparency log, ours included. This is the only place that reads the forge
while the log remembers what was published under the same name, so a forge copy
whose bytes disagree with the published tag is caught here and nowhere else. The
caller's `go.sum` pins everything a second time.

**Credentials stay in.** The forge token reaches git through the environment of
each child process — never a config file, never an image layer — and is removed
from any text on its way to a log or a response.

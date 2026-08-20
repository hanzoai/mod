# mod

The Go module proxy for the estate.

A module path is identity, not an address. `module github.com/hanzoai/o11y` is
the name every dependent's `go.mod` writes down, and changing it rewrites every
dependent. The source, meanwhile, lives on git.hanzo.ai. This service is where
those two facts meet: it answers for the name and reads from the forge, so a
build resolves the module it already names without dialing the host in the name.

## Configuration

Clients need two variables:

```sh
GOPROXY=http://mod.hanzo.svc.cluster.local
GONOSUMDB=github.com/hanzoai
```

`GOPROXY` is where modules come from. `GONOSUMDB` names the paths the public
checksum database must not be asked about — it cannot have seen them, and asking
publishes the path. It drops a lookup, not the verification: `go.sum` still pins
every module, including these, and a mismatch still fails the build.

Do not set `GOPRIVATE` for these paths. `GOPRIVATE` is shorthand for "bypass the
proxy AND the checksum database", and bypassing the proxy is what sends the
toolchain to github.com with a credential.

The server reads:

| variable | default | meaning |
|---|---|---|
| `FORGE` | `https://git.hanzo.ai` | where our source lives |
| `FORGE_TOKEN` | — | credential for the forge, from KMS |
| `NAMESPACES` | `github.com/hanzoai` | module prefixes this proxy answers for |
| `UPSTREAM` | `https://proxy.golang.org` | where everything else resolves |
| `ROOT` | `/var/lib/mod` | scratch module and module cache |
| `ADDR` | `:8080` | listen address |

## What it guarantees

**Our namespaces.** Resolved from the forge, which holds the source and its
tags. GitHub is not consulted and no GitHub credential is involved. This is
total, not best-effort: it does not matter what happens to the GitHub repository
— `github.com/hanzoai/s3-go` no longer exists there and still resolves here.

**Everything else.** Resolved from the public proxy and kept. Nothing prunes the
cache, so a module that came through once stays available if its origin
disappears. This covers what we have already served and nothing we have never
fetched — the difference between holding our own copy and trusting someone
else's retention.

**Paths stay in.** A module in our namespaces that cannot be resolved answers
502, never 404. In the GOPROXY protocol 404 means "ask the next proxy", and the
next proxy is public; 502 ends the walk. So a failure here cannot turn into the
name of an unreleased repository travelling to a third party.

**Credentials stay in.** The forge token reaches git through the environment of
each child process — never a config file, never an image layer — and is removed
from any text on its way to a log or a response.

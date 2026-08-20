# mod — working notes

The Go module proxy. Read `README.md` first for what it is and how it is
configured; this file holds the decisions behind it.

## Why this exists rather than the alternatives

Our private Go modules declare github paths, so the toolchain took a direct VCS
fetch to github.com and needed a credential valid there. Every GitHub credential
in the estate is a personal OAuth token. Three ways out were on the table:

1. **A proxy we own** — this. The module path is answered by us and read from the
   forge. One change in the client, no credential in the build, and module
   identity is untouched.
2. **Migrate the module paths** to a git.hanzo.ai host. Cleanest in principle,
   and the blast radius is why it is not the answer: 190 module definitions, 124
   dependent `go.mod` files, ~21,500 importing `.go` files in the hanzo tree
   alone, plus 33 required paths that are not even checked out locally.
3. **A machine credential at GitHub.** Keeps the dependency, only changes whose
   name is on it. It is the only option that serves genuinely public consumers of
   our modules — which is what the GitHub mirror is for — but it does nothing for
   our own builds.

The forge already had half of (1): `routers/api/packages/goproxy` in `hanzoai/git`
is a read-through cache of the public ecosystem with a server-side private-path
guard, and it is live. It cannot be the build-time proxy, for two reasons worth
keeping: its auth is package-scoped (the build's forge token is
`write:repository`, and broadening it is not on the table), and it is a registry
of what has been PUBLISHED to it — it has no way to resolve a module from the
repository whose source it is holding. Those are different jobs.

## The boundary that matters

`GOPRIVATE` is the wrong knob and reaching for it is the original bug. It means
"bypass the proxy AND the checksum database", and bypassing the proxy is exactly
what sends the toolchain to github.com. Clients set `GOPROXY` and `GONOSUMDB`
and nothing else.

Skipping the public checksum database for our namespaces is not skipping
verification. `go.sum` pins every module, ours included, and is checked against
the bytes served here. The proof is that the hashes this proxy produces are
byte-identical to the ones committed when GitHub served the same tags.

## The 404 rule

In the GOPROXY protocol 404 means "ask the next proxy". Inside our namespaces a
404 would therefore hand an unreleased repository's name to a public service. So
a failure inside our namespaces is 502 and the walk ends. `TestPrivateFailure
NeverFallsThrough` is the test that holds this; do not "fix" it to return 404.

## Retention

The module cache is the retention mechanism and nothing prunes it. Give `ROOT`
durable storage. This is not an optimization: `github.com/hanzoai/s3-go` is
required by `hanzoai/cloud`, `gateway` and `amqp`, and its GitHub repository no
longer exists. It resolves here because the forge holds it.

Two others are in the same state and this proxy does NOT save them, because
nothing holds them: `hanzoai/gh` (required by `docdb/tools`) and `hanzoai/xfail`
(required by `docdb/integration`) are absent from github.com, from the public
proxy, and from the forge. They need a source before any proxy can serve them;
their likely upstreams are `FerretDB/gh` and `FerretDB/xfail`, both public.

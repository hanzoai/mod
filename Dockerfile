# syntax=docker/dockerfile:1

# mod — the Go module proxy.
#
# The runtime keeps the Go toolchain and git, and that is deliberate: resolution
# is the toolchain's own. A module zip has rules about nested modules, vendor
# directories, symlinks, file modes and case collisions, and a zip that gets any
# of them wrong hashes differently and fails every caller's go.sum. Shipping the
# toolchain costs a bigger image and buys the one implementation of that which is
# known to be right.
#
# Built in CI on the self-hosted amd64 scale set, never on a developer's laptop.

FROM golang:1.26.5-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/mod .

FROM golang:1.26.5-bookworm
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/mod /usr/local/bin/mod
# ROOT holds the scratch module and the module cache. Mount durable storage
# here: the cache is what keeps a module available after its origin is gone, and
# nothing prunes it.
ENV ROOT=/var/lib/mod ADDR=:8080
RUN useradd --system --uid 10001 --home-dir /var/lib/mod --create-home mod
USER 10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/mod"]

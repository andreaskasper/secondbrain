# ---- build ----------------------------------------------------------------
# The Go version tracks src/go.mod (go 1.25.0). Bumping one without the other
# is how a build starts failing for a reason nobody can see in the diff.
FROM golang:1.26-alpine AS build

ARG VERSION=dev
ARG COMMIT=none
ARG BUILT=unknown

WORKDIR /src
COPY src/go.mod src/go.sum ./
RUN go mod download

COPY src/ ./
# CGO stays off: modernc.org/sqlite is a pure-Go SQLite, so the FTS5 index
# needs no libc and the binary can live in a scratch-like image.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.built=${BUILT}" \
      -o /secondbrain .

# ---- runtime --------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /secondbrain /secondbrain

# Unlike aegis, this container is not stateless: notes, the search index and
# one git repository per vault all live under /data. The image therefore
# cannot run with a read-only root filesystem, and /data must be writable by
# the distroless nonroot user, UID 65532. A bind mount owned by anyone else
# fails at startup - chown -R 65532:65532 the host directory first.
VOLUME /data

USER nonroot:nonroot
EXPOSE 2020

# Defaults that make `docker run -e SECONDBRAIN_USERNAME=... -e
# SECONDBRAIN_PASSWORD=... -e SECONDBRAIN_PUBLIC_URL=...` a complete command.
# SECONDBRAIN_CONFIG points at a file that need not exist: a single-user
# install is configured entirely from the environment.
ENV SECONDBRAIN_CONFIG=/etc/secondbrain/config.yaml \
    SECONDBRAIN_LISTEN=:2020 \
    SECONDBRAIN_DATA=/data \
    SECONDBRAIN_DEFAULT_VAULT=default

ENTRYPOINT ["/secondbrain"]

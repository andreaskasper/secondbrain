# ---- build stage ----
FROM golang:1.23-alpine AS build

WORKDIR /app/src
# Copy module files first to leverage layer caching (no external deps -> fast).
COPY src/go.mod ./
COPY src/ ./

# Static build so the final image can be FROM scratch.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /secondbrain .

# ---- runtime stage ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates wget && \
    adduser -D -u 10001 app

COPY --from=build /secondbrain /usr/local/bin/secondbrain

# Default data directory (mount a volume here).
ENV SECONDBRAIN_DATA_DIR=/data \
    SECONDBRAIN_ADDR=:8080
RUN mkdir -p /data && chown app:app /data
VOLUME ["/data"]

USER app
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/secondbrain"]

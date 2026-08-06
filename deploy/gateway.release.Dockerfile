# Dockerfile used by GoReleaser to package the pre-built binary.
# GoReleaser cross-compiles ferrogw for the target platform and copies it
# into the Docker build context before running this file, so there is no
# need to build from source here.
FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S ferro && adduser -S ferro -G ferro && \
    mkdir -p /app && chown ferro:ferro /app

# Pre-built binary is placed in the build context by GoReleaser, under a
# <goos>/<goarch> prefix — one build now covers every platform, so the copy has
# to name which one it is taking. TARGETPLATFORM is set by BuildKit and matches
# that layout ("linux/amd64", "linux/arm64").
ARG TARGETPLATFORM
COPY --chown=ferro:ferro ${TARGETPLATFORM}/ferrogw /bin/ferrogw

# Dependency licences, generated at release time and placed in the build context
# by GoReleaser's extra_files. /licenses is the OCI convention.
COPY notices/THIRD-PARTY-NOTICES.txt /licenses/

WORKDIR /app

EXPOSE 8080

USER ferro

# /readyz, not /livez: an orchestrator that only knows the process is alive will
# route traffic to an instance whose every target is unroutable. wget is in
# busybox, so this adds no package.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:8080/readyz || exit 1

ENTRYPOINT ["/bin/ferrogw"]

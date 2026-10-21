# Overridable so a build host that can't reach Docker Hub can use a mirror
# or a copy in its own registry, e.g.
#   --build-arg BUILD_IMAGE=gitea.internal:30008/nine9/golang:1.23-alpine
ARG BUILD_IMAGE=golang:1.23-alpine
FROM ${BUILD_IMAGE} AS build

WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web ./web
# Tests first: a failing test fails the image build.
RUN CGO_ENABLED=0 go test ./... \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/my-tasks . \
 && mkdir -p /out/data /out/tmp \
 && chmod 1777 /out/tmp

# One static binary, nothing else: the web UI is embedded in it.
FROM scratch
COPY --from=build /out/my-tasks /my-tasks
COPY --from=build --chown=1000:1000 /out/data /data
COPY --from=build /out/tmp /tmp

ENV DATA_DIR=/data \
    PORT=8099
USER 1000:1000
EXPOSE 8099
HEALTHCHECK --interval=60s --timeout=5s --start-period=5s CMD ["/my-tasks", "healthcheck"]
ENTRYPOINT ["/my-tasks"]

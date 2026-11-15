# Builds one of the cmd/ binaries into a distroless, non-root image.
#   docker build --build-arg CMD=triage-agent -t triage-agent:dev .
#   docker build --build-arg CMD=triage-controller -t triage-controller:dev .
FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY cmd ./cmd
COPY internal ./internal
ARG CMD
RUN test -n "$CMD" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app "./cmd/${CMD}"

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER 65532:65532
ENTRYPOINT ["/app"]

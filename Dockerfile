# Build the gwb-operator binary
FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Build
# GOARCH is left unset so the binary matches the builder host architecture. The caller can override it
# (e.g. via docker buildx --platform) to cross-compile for a target node architecture.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o gwb-operator cmd/main.go

# Distroless static:nonroot ships no shell, no package manager, and drops root — smallest attack surface
# for a single Go binary. See https://github.com/GoogleContainerTools/distroless.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/gwb-operator .
USER 65532:65532

ENTRYPOINT ["/gwb-operator"]

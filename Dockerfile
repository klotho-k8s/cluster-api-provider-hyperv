# Build the manager binary.
#
# NOTE: This manager must reach a Windows Hyper-V host and run its PowerShell
# cmdlets. A Linux container image CANNOT do that. This Dockerfile produces a
# Windows container (nanoserver) binary for host-agent deployments; for a single
# Hyper-V host the simplest option is to run the `manager` binary directly on the
# host rather than as a container. See the README.
FROM golang:1.25 AS builder
WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
COPY pkg/ pkg/
# Cross-compile a Windows binary.
ENV GOOS=windows GOARCH=amd64 CGO_ENABLED=0
RUN go build -o manager.exe ./cmd

FROM mcr.microsoft.com/windows/nanoserver:ltsc2022
WORKDIR /
COPY --from=builder /workspace/manager.exe /manager.exe
ENTRYPOINT ["\\manager.exe"]

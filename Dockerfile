# Multi-stage build for the illustration-operator

FROM golang:1.22-alpine AS builder
WORKDIR /workspace

# Install build tools
RUN apk add --no-cache git ca-certificates && update-ca-certificates

# Copy go modules and download deps
COPY go.mod ./
RUN go mod download

# Copy the rest of the source
COPY . ./

# Build the manager binary. When using buildx with a specific platform,
# TARGETOS/TARGETARCH will be set accordingly (e.g. linux/arm64).
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o manager main.go

# Final minimal image
FROM gcr.io/distroless/base-debian12
WORKDIR /
COPY --from=builder /workspace/manager /manager

USER 65532:65532

ENTRYPOINT ["/manager"]

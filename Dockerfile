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

# Build the manager binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o manager main.go

# Final minimal image
FROM gcr.io/distroless/base-debian12
WORKDIR /
COPY --from=builder /workspace/manager /manager

USER 65532:65532

ENTRYPOINT ["/manager"]

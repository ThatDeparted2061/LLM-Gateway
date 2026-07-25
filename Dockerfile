# The build stage runs on the builder's native platform and cross-compiles, so
# multi-arch images (linux/amd64 + linux/arm64) need no QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /gateway ./cmd/gateway

# distroless/static ships CA certificates (needed for Groq/Gemini HTTPS) and runs as nonroot.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /gateway /gateway
EXPOSE 8080
# Configure with env vars, or mount a file and pass: -config /config.yaml
ENTRYPOINT ["/gateway"]

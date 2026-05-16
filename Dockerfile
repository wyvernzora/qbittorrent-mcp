## Build stage
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src

# Pre-fetch dependencies for layer caching.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/qbit-mcp \
    ./cmd/qbit-mcp

## Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/qbit-mcp /usr/local/bin/qbit-mcp

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/qbit-mcp"]
CMD ["--transport=http", "--addr=:8080"]

# Build a static binary, then ship it in a minimal image with CA certificates.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /claude-usage .

FROM gcr.io/distroless/static-debian12
COPY --from=build /claude-usage /claude-usage
VOLUME ["/data"]
EXPOSE 8787
ENTRYPOINT ["/claude-usage", "serve", "--listen", ":8787", "--data-dir", "/data", "--credentials", "/secrets/credentials.json"]

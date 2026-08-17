# --- build stage ---
FROM golang:1.22-alpine AS build
WORKDIR /src
# Only go.mod for now (stdlib-only, no go.sum yet). Add go.sum when deps arrive.
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /chat-relay .

# --- runtime stage: distroless, non-root, static ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /chat-relay /chat-relay
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/chat-relay"]

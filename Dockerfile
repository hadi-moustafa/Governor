# Builds the gateway server only (cmd/governor) — governorctl is a local
# terminal tool, not something you'd containerize.
FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/governor ./cmd/governor

# distroless: no shell, no package manager — smaller attack surface than
# alpine for a binary that has no runtime dependencies of its own.
FROM gcr.io/distroless/static-debian12

COPY --from=builder /out/governor /governor

EXPOSE 8080

ENTRYPOINT ["/governor"]

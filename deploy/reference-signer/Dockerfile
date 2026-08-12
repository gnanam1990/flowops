FROM golang:1.26.5-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/reference-signer ./cmd/reference-signer

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/reference-signer /usr/local/bin/reference-signer
ENTRYPOINT ["/usr/local/bin/reference-signer"]

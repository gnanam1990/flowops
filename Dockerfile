FROM golang:1.26-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build

WORKDIR /src
# Required source claim for Base mainnet. Admission also hashes the running
# executable and compares it with the offline-signed artifact digest.
ARG FLOWOPS_SOURCE_COMMIT=unversioned
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg
RUN test "${FLOWOPS_SOURCE_COMMIT}" = unversioned || { \
	test "${#FLOWOPS_SOURCE_COMMIT}" -eq 40 && \
	case "${FLOWOPS_SOURCE_COMMIT}" in *[!0-9a-f]*) false ;; *) true ;; esac; \
}
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X=main.buildSourceCommit=${FLOWOPS_SOURCE_COMMIT}" -o /out/control-plane-api ./cmd/control-plane-api && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/flowops-admin ./cmd/flowops-admin && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/flowops-operator ./cmd/flowops-operator && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ascp-leadership ./cmd/ascp-leadership && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ascp-seller-worker ./cmd/ascp-seller-worker && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ascp-event-recovery ./cmd/ascp-event-recovery && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ascp-verifier ./cmd/ascp-verifier && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ascp-keeper ./cmd/ascp-keeper && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ascp-governance-relayer ./cmd/ascp-governance-relayer && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ascp-bearer-worker ./cmd/ascp-bearer-worker && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ascp-signer-runtime ./cmd/ascp-signer-runtime && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ascp-ring6-runtime ./cmd/ascp-ring6-runtime && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ascp-asset-health ./cmd/ascp-asset-health && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ascp-capacity-audit ./cmd/ascp-capacity-audit && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/postgres-readiness ./cmd/postgres-readiness

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40

RUN apk add --no-cache ca-certificates su-exec && \
    addgroup -S -g 10001 flowops && \
    adduser -S -D -H -u 10001 -G flowops flowops && \
    install -d -m 0700 -o flowops -g flowops /var/lib/flowops /flowops
COPY --from=build /out/control-plane-api /out/flowops-admin /out/flowops-operator /out/ascp-leadership /out/ascp-seller-worker /out/ascp-event-recovery /out/ascp-verifier /out/ascp-keeper /out/ascp-governance-relayer /out/ascp-bearer-worker /out/ascp-signer-runtime /out/ascp-ring6-runtime /out/ascp-asset-health /out/ascp-capacity-audit /out/postgres-readiness /flowops/
COPY deploy/control-plane/entrypoint.sh /flowops/entrypoint.sh
RUN chmod 0555 /flowops/control-plane-api /flowops/flowops-admin /flowops/flowops-operator /flowops/ascp-leadership /flowops/ascp-seller-worker /flowops/ascp-event-recovery /flowops/ascp-verifier /flowops/ascp-keeper /flowops/ascp-governance-relayer /flowops/ascp-bearer-worker /flowops/ascp-signer-runtime /flowops/ascp-ring6-runtime /flowops/ascp-asset-health /flowops/ascp-capacity-audit /flowops/postgres-readiness /flowops/entrypoint.sh

EXPOSE 8080 8082
ENTRYPOINT ["/flowops/entrypoint.sh"]

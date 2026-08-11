.PHONY: test check fmt-check smoke-x402-readonly smoke-evidence-fetch

test:
	go test -race ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

check: fmt-check
	go vet ./...
	go test -race ./...

smoke-x402-readonly:
	go run ./cmd/x402-conformance

smoke-evidence-fetch:
	go test -race -run '^TestHandlerSmoke$$' ./internal/evidencefetch

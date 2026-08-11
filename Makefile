.PHONY: test check fmt-check

test:
	go test -race ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

check: fmt-check
	go vet ./...
	go test -race ./...

.PHONY: build test race lint vet fmt run clean

# CGO_ENABLED=0 is a project invariant: it is what keeps cross-compilation to
# macOS and Linux possible from a single machine. See the design notes under
# .ktools/.
export CGO_ENABLED = 0

BIN := bin/musem

build:
	go build -o $(BIN) ./cmd/musem

test:
	go test ./...

# The race detector needs cgo, so it cannot run under the project-wide
# CGO_ENABLED=0. That invariant governs the shipped binary, not the test runner.
race:
	CGO_ENABLED=1 go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run

fmt:
	gofmt -l -w .

run: build
	./$(BIN)

clean:
	rm -rf bin

# Unix Makefile (macOS/Linux/WSL2). Native Windows has no `make` — build with
# `go build -o bin\aisr.exe ./cmd/aisr` instead; see docs/windows.md.
BIN := bin/aisr

.PHONY: build vet test run clean

build:
	go build -o $(BIN) ./cmd/aisr

vet:
	go vet ./...

test:
	go test ./...

run: build
	./$(BIN) $(ARGS)

clean:
	rm -rf bin

# Unix Makefile (macOS/Linux/WSL2). Native Windows has no `make` — build with
# `go build -o bin\aisr.exe ./cmd/aisr` instead; see docs/windows.md.
BIN := bin/aisr
LDFLAGS := -s -w

.PHONY: build vet test run clean windows

build:
	go build -o $(BIN) ./cmd/aisr

# Cross-compile Windows binaries (amd64 + arm64) into dist/.
windows:
	mkdir -p dist
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/aisr-windows-amd64.exe ./cmd/aisr
	GOOS=windows GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/aisr-windows-arm64.exe ./cmd/aisr
	@echo "built: dist/aisr-windows-amd64.exe dist/aisr-windows-arm64.exe"

vet:
	go vet ./...

test:
	go test ./...

run: build
	./$(BIN) $(ARGS)

clean:
	rm -rf bin

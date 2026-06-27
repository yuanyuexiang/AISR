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

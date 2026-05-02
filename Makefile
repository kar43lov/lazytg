.PHONY: build test lint clean tidy help

# Default target
help:
	@echo "Available targets:"
	@echo "  build  - build lazytg binary into bin/"
	@echo "  test   - run all tests with race detector"
	@echo "  lint   - run golangci-lint"
	@echo "  clean  - remove build artefacts"
	@echo "  tidy   - run go mod tidy"

build:
	go build -o bin/lazytg ./cmd/lazytg

test:
	go test -race ./...

lint:
	golangci-lint run

clean:
	rm -rf bin/ dist/ coverage.out

tidy:
	go mod tidy

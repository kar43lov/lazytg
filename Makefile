.PHONY: build test bench lint clean tidy help

# Default target
help:
	@echo "Available targets:"
	@echo "  build  - build lazytg binary into bin/"
	@echo "  test   - run all tests with race detector"
	@echo "  bench  - run the FTS5 search p95 SLA gate (BenchmarkSearch100k)"
	@echo "  lint   - run golangci-lint"
	@echo "  clean  - remove build artefacts"
	@echo "  tidy   - run go mod tidy"

build:
	go build -o bin/lazytg ./cmd/lazytg

test:
	go test -race ./...

# bench runs the search SLA gate. It fails the build if p95 > 100 ms on the
# 100k-message synthetic corpus — the product SLA, which this target checks
# on your own machine. CI runs the same target with LAZYTG_BENCH_P95_MS=250
# because a shared runner measures ~115 ms for code that does 47 ms here;
# see docs/PERFORMANCE.md. -benchtime=1x produces the single 100-sample
# window the bench's assertions expect; -run=^$ skips the test functions so
# we measure only BenchmarkSearch100k.
bench:
	go test -bench=BenchmarkSearch100k -benchtime=1x -run=^$$ ./internal/core/search/

lint:
	golangci-lint run

clean:
	rm -rf bin/ dist/ coverage.out

tidy:
	go mod tidy

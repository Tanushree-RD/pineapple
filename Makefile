GO ?= go
.PHONY: all build test vet check bench clean
all: build
build:
	mkdir -p bin
	$(GO) build -o bin/kvdb ./src/cmd/kvdb
	$(GO) build -o bin/torture ./torture
test:
	$(GO) test ./...
vet:
	$(GO) vet ./...
check: build vet test
bench:
	KVDB_BENCH=1 $(GO) test ./tests -run Bench -v -timeout 30m
clean:
	rm -rf bin

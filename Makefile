GO ?= go
.PHONY: all build test vet check bench clean ui
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
# The friendly front ends (not part of the judged build): bin/leapfrog and bin/hydra.
ui: build
	$(GO) build -o bin/leapfrog ./src/cmd/leapfrog
	$(GO) build -o bin/hydra ./src/cmd/hydra

GO ?= go
.PHONY: all build test vet check bench clean ui preflight
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
	$(GO) build -o bin/pineapple ./src/cmd/pineapple

# Run this ON THE DEMO LAPTOP before the demo: a short pass over every judge-facing check, with a fresh seed
# each time, so a pass here is a pass for a seed nobody has used before. It stops at the first failure.
preflight: build
	@S=$$(date +%s); echo "preflight seed $$S"; \
	./bin/kvdb > /dev/null && \
	./bin/torture --iterations 50 --seed $$S && \
	./bin/torture --mode compaction --iterations 20 --seed $$S && \
	./bin/torture --mode recovery-crash --iterations 8 --seed $$S && \
	./bin/torture --mode fixtures && \
	./bin/torture --mode sweep --seed $$S && \
	echo "PREFLIGHT PASSED on this machine (seed $$S)"


.PHONY: help test race bench bench-full vet fmt check cover clean

GO ?= go

help:
	@echo "Available targets:"
	@echo "  make test        Run all unit tests"
	@echo "  make race        Run race detector"
	@echo "  make bench       Run benchmarks"
	@echo "  make bench-full  Run repeated benchmarks"
	@echo "  make vet         Run go vet"
	@echo "  make fmt         Format Go source"
	@echo "  make check       Run release checks"
	@echo "  make cover       Generate coverage report"
	@echo "  make clean       Remove generated reports"

test:
	$(GO) test -count=1 ./...

race:
	$(GO) test -race -count=1 ./...

bench:
	$(GO) test -run='^$$' -bench=. -benchmem ./...

bench-full:
	$(GO) test -run='^$$' -bench=. -benchmem -benchtime=5s -count=5 ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

check: vet race

cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

clean:
	rm -f coverage.out cpu.prof mem.prof


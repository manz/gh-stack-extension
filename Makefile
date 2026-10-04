COVERAGE_MIN := 80
COVER_PROFILE := coverage.out

.PHONY: build test cover lint check

build:  ## Build the extension binary
	go build -o gh-stack-extension .

test:  ## Run the tests
	go test ./...

cover:  ## Run the tests and fail under COVERAGE_MIN percent total coverage
	go test -coverprofile=$(COVER_PROFILE) ./...
	@total=$$(go tool cover -func=$(COVER_PROFILE) | awk '/^total:/ {sub("%", "", $$3); print $$3}'); \
	echo "total coverage: $$total% (minimum $(COVERAGE_MIN)%)"; \
	awk -v t="$$total" -v m="$(COVERAGE_MIN)" 'BEGIN { exit (t + 0 < m) }' || { echo "coverage below $(COVERAGE_MIN)%"; exit 1; }

lint:  ## gofmt and go vet
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "gofmt needed: $$unformatted"; exit 1; fi
	go vet ./...

check: lint cover  ## Everything CI runs

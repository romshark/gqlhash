# Targets for testing this module. See README "Development".

# GOCOVERDIR has to be absolute: the servers the acceptance suite starts inherit
# the working directory of the test binary, which is the package under test.
COVERDIR := $(CURDIR)/covdata

# all is what a change is checked with: lint, then every test, then what they
# covered, then the race detector. cover runs the tests itself, which is why
# test isn't in the list.
.PHONY: all
all: lint cover race

# -shuffle=on runs the tests in a random order and prints the seed it used.
# The acceptance suite shares a server between the tests that need no flags of their own,
# so an order they depend on is a bug this is meant to find.
SHUFFLE := -shuffle=on

.PHONY: test
test:
	@echo "== test =="
	go test $(SHUFFLE) ./...

# race leaves out the acceptance suite on purpose. -race instruments the test binary,
# and the suite's servers are separate processes built without it,
# so the run would cost the full suite's time to check only the harness —
# which does its own locking. The concurrent code that matters is in the packages
# below, where the tests reach it in process. To instrument the servers too,
# resolve() in the harness has to pass -race to the builds it does.
.PHONY: race
race:
	@echo "== test: the race detector =="
	go test $(SHUFFLE) -race -count=1 $$(go list ./... | grep -v /internal/acceptance)

.PHONY: lint
lint:
	@echo "== lint =="
	@files=$$(gofmt -l .); if [ -n "$$files" ]; then \
		echo "gofmt wants:"; echo "$$files"; exit 1; fi
	go vet ./...
	@if command -v golangci-lint >/dev/null; then \
		golangci-lint run ./...; \
	else echo "golangci-lint not installed, skipping"; fi

# acceptance runs the suite against the commands of this repository,
# or against another implementation of the same contract:
#
#	make acceptance PROXY=/path/to/your-proxy
#
# FHTTP=0 leaves out gqlhash-proxy-fhttp, the experimental fasthttp build,
# which halves the runtime. Every rule it keeps is one gqlhash-proxy keeps too,
# so this is the run to do while working on a rule and the full one to do
# before committing.
#
#	make acceptance FHTTP=0
.PHONY: acceptance
acceptance:
	@echo "== test: the acceptance suite =="
	go test $(SHUFFLE) -count=1 ./internal/acceptance \
		$(if $(PROXY),-proxy.bin $(PROXY),) \
		$(if $(filter 0 no false,$(FHTTP)),-proxy.fhttp=false,)

.PHONY: cover
cover: cover-unit cover-servers

# cover-unit is what the tests reach in process.
.PHONY: cover-unit
cover-unit:
	@echo "== test: everything, in process, with coverage =="
	go test $(SHUFFLE) -covermode=count -coverprofile=unit.out ./...
	@echo "== coverage: in process =="
	@go tool cover -func=unit.out | tail -n 1

# cover-servers is what the servers the acceptance suite starts reach. It takes
# no -cover flag: go test would then set GOCOVERDIR itself and the servers would
# write their counters into its temporary directory instead of COVERDIR.
.PHONY: cover-servers
cover-servers:
	@echo "== test: the acceptance suite, with the servers instrumented =="
	@mkdir -p $(COVERDIR)
	GOCOVERDIR=$(COVERDIR) go test $(SHUFFLE) -count=1 ./internal/acceptance
	@echo "== coverage: the servers the acceptance suite started =="
	@go tool covdata percent -i=$(COVERDIR)
# cover-profile turns the servers' counters into a profile,
# for a tool that reads one.
.PHONY: cover-profile
cover-profile: cover-servers
	go tool covdata textfmt -i=$(COVERDIR) -o=acceptance.out

.PHONY: clean
clean:
	rm -rf $(COVERDIR) unit.out acceptance.out

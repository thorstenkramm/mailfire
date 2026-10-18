BINARY  := mailfire
# Tags are expected as vX.Y.Z; the leading "v" is dropped so the version
# subcommand prints "mailfire 0.3.1 (...)" as specified in FR-6.
VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null || echo dev))
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%d)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(DATE)
# Google OAuth client secret of the built-in Gmail client: never committed.
# Taken from GMAIL_CLIENT_SECRET, else from the client file downloaded from
# the Google Cloud console into tmp/. Without it, Gmail sign-in needs
# MAILFIRE_GMAIL_CLIENT_SECRET at run time.
GMAIL_CLIENT_SECRET ?= $(shell sed -n 's/.*"client_secret": *"\([^"]*\)".*/\1/p' tmp/client_secret_*.json 2>/dev/null | head -n 1)
ifneq ($(GMAIL_CLIENT_SECRET),)
LDFLAGS += -X github.com/thorstenkramm/mailfire/internal/gmail.builtinClientSecret=$(GMAIL_CLIENT_SECRET)
endif

.PHONY: build test lint coverage clean docs docs-man docs-serve

COVERAGE_MIN := 85

build:
	@echo "go build $(BINARY)$(if $(GMAIL_CLIENT_SECRET), (with Gmail client secret),)"
	@CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $(BINARY) .

test:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l .)" || { echo "gofmt: files need formatting:"; gofmt -l .; exit 1; }
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...

coverage:
	go test -coverprofile=coverage.out $$(go list ./... | grep -v /internal/testutil)
	go tool cover -func=coverage.out
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ {sub("%", "", $$3); print $$3}'); \
	echo "total coverage: $$total% (minimum $(COVERAGE_MIN)%)"; \
	awk -v t="$$total" -v min="$(COVERAGE_MIN)" 'BEGIN { exit (t + 0 >= min + 0) ? 0 : 1 }' \
		|| { echo "FAIL: total coverage $$total% is below $(COVERAGE_MIN)%"; exit 1; }

# Documentation site (Hugo, docs/). The man page is rendered to an HTML
# fragment by mandoc first; the production build fails without it.
DOCS_MAN := docs/assets/generated/mailfire.1.html

docs-man:
	@mkdir -p $(dir $(DOCS_MAN))
	mandoc -T html -O fragment docs/mailfire.1 > $(DOCS_MAN)

docs: docs-man
	hugo --source docs --minify --gc --cleanDestinationDir --panicOnWarning

docs-serve: docs-man
	hugo server --source docs

clean:
	rm -f $(BINARY) coverage.out
	rm -rf docs/public docs/resources docs/.hugo_build.lock $(dir $(DOCS_MAN))

.PHONY: build test lint integration endpoints docs snapshot

build:
	CGO_ENABLED=0 go build -trimpath -o forgejo-mcp ./cmd/forgejo-mcp

test:
	go test ./...

lint:
	golangci-lint config verify
	golangci-lint run ./...

# Starts the throwaway Forgejo container, runs the integration tests and
# always tears the container down again.
integration:
	@status=0; \
	eval "$$(bash test/integration/bootstrap.sh | grep '^export ')" && go test ./test/integration -count=1 || status=$$?; \
	bash test/integration/teardown.sh; \
	exit $$status

# Usage: make endpoints SWAGGER=path/or/url/to/swagger.json [VER=17.0.2]
endpoints:
	@test -n "$(SWAGGER)" || (echo "usage: make endpoints SWAGGER=<file or url> [VER=17.0.2]"; exit 2)
	go run ./internal/endpoints/gen -in "$(SWAGGER)" -version "$(or $(VER),17.0.2)" -out internal/endpoints/index.json

# Regenerates docs/TOOLS.md from the tool registry.
docs:
	go run ./internal/tools/docgen

snapshot:
	goreleaser release --snapshot --clean --skip=publish

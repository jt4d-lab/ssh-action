# Version baked into the binary: the closest git tag, or "dev" outside a tag.
VER ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo dev)
LDFLAGS = -s -w -X main.version=$(VER)
ARCHES = amd64 arm64
# DIST_DIR lets release-check build the same artifacts outside dist/.
DIST_DIR ?= dist

.PHONY: build test lint dist release-check

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ssh-action ./cmd/ssh-action

test:
	go test ./...

lint:
	go vet ./...
	shellcheck action/entrypoint.sh tests/entrypoint_test.sh tests/dist_test.sh

# dist holds the two committed static linux binaries that entrypoint.sh unpacks.
dist:
	mkdir -p $(DIST_DIR)
	@for arch in $(ARCHES); do \
		echo "building linux/$$arch $(VER)"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/linux-$$arch ./cmd/ssh-action || exit 1; \
		gzip -9n -c $(DIST_DIR)/linux-$$arch >$(DIST_DIR)/linux-$$arch.gz || exit 1; \
		rm -f $(DIST_DIR)/linux-$$arch; \
	done
	cd $(DIST_DIR) && sha256sum linux-amd64.gz linux-arm64.gz >checksums.txt

# release-check rebuilds the binaries into a scratch directory and compares the
# checksums, so a stale committed dist/ or a source change without make dist
# fails before a release is tagged.
release-check:
	@set -eu; \
	scratch=$$(mktemp -d); \
	trap 'rm -rf "$$scratch"' EXIT; \
	$(MAKE) --no-print-directory dist DIST_DIR="$$scratch"; \
	cmp dist/checksums.txt "$$scratch/checksums.txt" || { \
		echo "release-check: dist/ differs from a fresh build, run make dist" >&2; \
		exit 1; \
	}; \
	echo "release-check: dist/ matches a fresh build"

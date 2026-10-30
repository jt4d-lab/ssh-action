# Version baked into the binary: the closest git tag, or "dev" outside a tag.
VER ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo dev)
LDFLAGS = -s -w -X main.version=$(VER)
ARCHES = amd64 arm64

.PHONY: build test lint dist

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ssh-action ./cmd/ssh-action

test:
	go test ./...

lint:
	go vet ./...
	shellcheck action/entrypoint.sh tests/entrypoint_test.sh

# dist holds the two committed static linux binaries that entrypoint.sh unpacks.
dist:
	mkdir -p dist
	@for arch in $(ARCHES); do \
		echo "building linux/$$arch $(VER)"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/linux-$$arch ./cmd/ssh-action || exit 1; \
		gzip -9n -c dist/linux-$$arch >dist/linux-$$arch.gz || exit 1; \
		rm -f dist/linux-$$arch; \
	done
	cd dist && sha256sum linux-amd64.gz linux-arm64.gz >checksums.txt

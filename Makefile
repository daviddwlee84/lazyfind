.PHONY: build test race vet check fmt

# Explicit VERSION wins. Otherwise retain an exact local tag, or identify the
# checkout; only tracked changes add -dirty (untracked notes do not affect it).
VERSION ?=
BUILD_VERSION := $(if $(strip $(VERSION)),$(strip $(VERSION)),$(shell \
	build_tag=$$(git describe --tags --exact-match 2>/dev/null); \
	build_revision=$$(git rev-parse --short HEAD 2>/dev/null); \
	if [ -n "$$build_tag" ]; then build_version=$$build_tag; \
	elif [ -n "$$build_revision" ]; then build_version=dev+$$build_revision; \
	else build_version=dev; fi; \
	if [ -n "$$build_revision" ] && ! git diff --quiet HEAD -- 2>/dev/null; then build_version=$$build_version-dirty; fi; \
	printf '%s' "$$build_version"))
export LAZYFIND_BUILD_VERSION := $(BUILD_VERSION)

build:
	go build -ldflags "-X main.version=$$LAZYFIND_BUILD_VERSION" -o bin/lazyfind .
test:
	go test ./...
race:
	go test -race ./...
vet:
	go vet ./...
check: test vet
fmt:
	gofmt -w main.go internal

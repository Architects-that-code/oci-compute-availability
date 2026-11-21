.DEFAULT_GOAL := help
VERSION ?= v0.1.1
IMAGE ?= ghcr.io/architects-that-code/oci-compute-availability:$(VERSION)
PLATFORMS ?= linux/amd64,linux/arm64
.PHONY: help test build image publish
help:
	@printf '%s\n' \
	  'OCI Compute availability' \
	  '' \
	  'Usage: make [target] [VARIABLE=value ...]' \
	  '' \
	  'Targets:' \
	  '  help     Print this help (default when running make)' \
	  '  test     Run Go race tests and go vet' \
	  '  build    Test and build dist/collector' \
	  '  image    Test and create dist/collector.oci.tar with Docker Buildx' \
	  '  publish  Test, build and push both architectures to GHCR' \
	  '' \
	  'Settings (current values):' \
	  '  VERSION=$(VERSION)' \
	  '  IMAGE=$(IMAGE)' \
	  '  PLATFORMS=$(PLATFORMS)' \
	  '' \
	  'Examples:' \
	  '  make build' \
	  '  make image VERSION=v0.1.1' \
	  '  make publish VERSION=v0.1.1' \
	  '' \
	  'Collector runtime help: ./dist/collector --help'
test:
	cd collector && go test -race ./... && go vet ./...
build: test
	mkdir -p dist
	cd collector && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../dist/collector .
image: test
	mkdir -p dist
	docker buildx build --platform $(PLATFORMS) --provenance=false --tag "$(IMAGE)" --output type=oci,dest=dist/collector.oci.tar .
publish: test
	IMAGE="$(IMAGE)" bash scripts/publish-image.sh

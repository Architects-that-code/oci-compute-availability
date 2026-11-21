.DEFAULT_GOAL := help
VERSION ?= v0.1.0
IMAGE ?= ghcr.io/architects-that-code/oci-compute-availability:$(VERSION)
PLATFORMS ?= linux/amd64,linux/arm64
.PHONY: help test build image publish
help:
	@printf '%s\n' 'make test | build | image | publish [VERSION=v0.1.0]' 'build creates dist/collector; image creates a local OCI archive; publish pushes both architectures to GHCR.'
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

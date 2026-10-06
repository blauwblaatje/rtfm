# RTFM, the Roller derby Tournament Fixture Maker.
#
#   make            build bin/rtfm
#   make run        run it on :8080 with the blank statsbooks in blank/
#   make test       vet and tests
#   make vendor     refresh vendor/ from ../crg-format (after changing it there)
#   make image      the container image (docker build)
#   make image-multi  amd64 + arm64 image, pushed to IMAGE (docker buildx)
#   make k3s-import load the image into a local k3s (no registry needed)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE   ?= rtfm
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all run test vendor image image-multi k3s-import clean

all:
	@mkdir -p bin
	CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags '$(LDFLAGS)' -o bin/rtfm ./cmd/rtfm

run: all
	RTFM_BLANK=blank RTFM_DATA=data ./bin/rtfm

test:
	go vet -mod=vendor ./cmd/... ./internal/...
	go test -mod=vendor ./cmd/... ./internal/...

vendor:
	go mod tidy
	go mod vendor
	rm -f vendor/crgformat/crgserver

image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

image-multi:
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest --push .

k3s-import: image
	docker save $(IMAGE):latest | sudo k3s ctr images import -

clean:
	rm -rf bin

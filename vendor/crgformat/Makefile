# Builds the CRG tools for every platform the scoreboard runs on. Everything
# is pure Go (no cgo), so all targets cross-compile from one machine.
#
#   make            the tools for this machine, in bin/
#   make test       vet and tests
#   make dist       every platform, archived in dist/
#   make linux-amd64 (or any name in PLATFORMS)   one platform, in dist/
#   make clean

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
CMDS     := crgserver crgproxy crgconvert crgreplay crgreview crgstatsbook
LDFLAGS  := -s -w -X main.version=$(VERSION)
GOFLAGS  := -trimpath
DOCS     := README.md LICENSE NOTICE

# name:GOOS:GOARCH:GOARM. Raspberry Pi OS 64-bit is linux-arm64 (the
# rpi-arm64 archive is the same build under the name Pi users look for);
# 32-bit Raspberry Pi OS on a Pi 2 or newer is armv7.
PLATFORMS := \
	linux-amd64:linux:amd64: \
	linux-arm64:linux:arm64: \
	windows-amd64:windows:amd64: \
	windows-arm64:windows:arm64: \
	macos-amd64:darwin:amd64: \
	macos-arm64:darwin:arm64: \
	rpi-arm64:linux:arm64: \
	rpi-armv7:linux:arm:7

NAMES := $(foreach p,$(PLATFORMS),$(firstword $(subst :, ,$(p))))

.PHONY: all test dist clean $(NAMES)

all:
	@mkdir -p bin
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/ $(addprefix ./cmd/,$(CMDS))

test:
	go vet ./...
	go test ./...

dist: $(NAMES)
	cd dist && sha256sum crg-$(VERSION)-* > SHA256SUMS

# One rule per platform: build every command into dist/<dir>, then archive
# it (zip for Windows, tar.gz for the rest).
define platform
$(1):
	@echo "== $(1)"
	@rm -rf dist/crg-$(VERSION)-$(1)
	@mkdir -p dist/crg-$(VERSION)-$(1)
	@for c in $(CMDS); do \
		CGO_ENABLED=0 GOOS=$(2) GOARCH=$(3) GOARM=$(4) go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
			-o dist/crg-$(VERSION)-$(1)/$$$$c$(if $(filter windows,$(2)),.exe) ./cmd/$$$$c || exit 1; \
	done
	@cp $(DOCS) dist/crg-$(VERSION)-$(1)/
	@cd dist && $(if $(filter windows,$(2)),zip -qr crg-$(VERSION)-$(1).zip,tar czf crg-$(VERSION)-$(1).tar.gz) crg-$(VERSION)-$(1)
	@rm -rf dist/crg-$(VERSION)-$(1)
endef

$(foreach p,$(PLATFORMS),$(eval $(call platform,$(word 1,$(subst :, ,$(p))),$(word 2,$(subst :, ,$(p))),$(word 3,$(subst :, ,$(p))),$(word 4,$(subst :, ,$(p))))))

clean:
	rm -rf dist bin/crg*

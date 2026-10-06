# RTFM in a container: a static Go binary on distroless, as a non-root user.
# Build: docker build -t rtfm .   (put WFTDA's blank statsbooks in blank/ first)
# Multi-arch (amd64, arm64 for a Raspberry Pi k3s node): make image-multi
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=vendor -trimpath \
      -ldflags "-s -w -X main.version=$VERSION" -o /out/rtfm ./cmd/rtfm
# The blank statsbooks, if blank/ has them (it may not: then mount a folder).
RUN mkdir -p /out/data /out/blank && find blank -name '*.xlsx' -exec cp {} /out/blank/ \;

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/rtfm /rtfm
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build /out/blank /blank
ENV RTFM_ADDR=:8080 RTFM_BLANK=/blank RTFM_DATA=/data
EXPOSE 8080
VOLUME /data
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s CMD ["/rtfm", "-health"]
ENTRYPOINT ["/rtfm"]

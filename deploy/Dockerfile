FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/borisovdev/newsbuddy/internal/logging.Version=${VERSION}" \
    -o /out/news-buddy ./cmd/news-buddy
# distroless has no shell, so the nonroot-owned /data mountpoint is prepared
# here; a fresh named volume inherits this ownership.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/news-buddy /usr/local/bin/news-buddy
COPY --from=build --chown=65532:65532 /out/data /data
USER nonroot:nonroot
ENV DB_PATH=/data/news-buddy.db
VOLUME /data
ENTRYPOINT ["/usr/local/bin/news-buddy"]

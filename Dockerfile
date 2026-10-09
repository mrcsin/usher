# syntax=docker/dockerfile:1

# Build and runtime images use the same alpine tag as awg-grpc.
FROM golang:1.27.1-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY gen ./gen
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/usher ./cmd/usher

FROM alpine:3.24.2 AS runtime
COPY --from=build /out/usher /usr/local/bin/usher
RUN mkdir -p /srv/usher/config /srv/usher/clients /srv/usher/state \
 && chown 1000:1000 /srv/usher/clients /srv/usher/state
USER 1000:1000
ENTRYPOINT ["usher"]
CMD ["run"]

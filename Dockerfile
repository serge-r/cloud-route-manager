# syntax=docker/dockerfile:1
ARG GO_VERSION=1.26
ARG ALPINE_VERSION=3.21

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
    -o /cloud-route-manager .

FROM alpine:${ALPINE_VERSION}
RUN apk --update --no-cache add ca-certificates
COPY --from=build /cloud-route-manager /opt/cloud-route-manager/bin/cloud-route-manager
COPY config.example.yml /opt/cloud-route-manager/config.example.yml

ENTRYPOINT ["/opt/cloud-route-manager/bin/cloud-route-manager"]
CMD ["-config", "/opt/cloud-route-manager/config.yml"]

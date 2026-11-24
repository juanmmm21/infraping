# syntax=docker/dockerfile:1
# La etapa de compilación corre en la arquitectura del host y compila cruzado: así la imagen
# arm64 no necesita emulación QEMU, que multiplica el tiempo de build.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/infraping ./cmd/infraping
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/juanmmm21/infraping" \
      org.opencontainers.image.description="Self-hosted uptime monitoring in a single Go binary" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/infraping /usr/local/bin/infraping
# /data tiene que ser del usuario sin privilegios de distroless, o no se podría guardar el historial.
COPY --from=build --chown=65532:65532 /out/data /data
WORKDIR /data
ENV INFRAPING_CONFIG=/config/infraping.yaml
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/infraping"]
CMD ["run"]

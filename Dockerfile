# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/infraping ./cmd/infraping
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/infraping /usr/local/bin/infraping
# /data tiene que ser del usuario sin privilegios de distroless, o no se podría guardar el historial.
COPY --from=build --chown=65532:65532 /out/data /data
WORKDIR /data
ENV INFRAPING_CONFIG=/config/infraping.yaml
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/infraping"]
CMD ["run"]

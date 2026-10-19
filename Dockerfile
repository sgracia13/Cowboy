# cowboy: one small binary, no external services.
FROM golang:1.24-alpine AS build
WORKDIR /src
ARG VERSION=dev
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /cowboy ./cmd/cowboy

FROM alpine:3.20
LABEL org.opencontainers.image.source="https://github.com/sgracia13/Cowboy" \
      org.opencontainers.image.description="Sorts AI-agent pull requests into easy, normal and careful. Self-hosted." \
      org.opencontainers.image.licenses="AGPL-3.0-only"
RUN adduser -D -u 10001 cowboy && mkdir /data && chown cowboy /data
COPY --from=build /cowboy /usr/local/bin/cowboy
COPY testdata /opt/cowboy/testdata
USER cowboy
WORKDIR /opt/cowboy
ENV DATA=/data/cowboy.json ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
VOLUME /data
ENTRYPOINT ["cowboy"]
CMD ["serve"]

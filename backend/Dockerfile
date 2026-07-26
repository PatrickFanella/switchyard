FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
WORKDIR /src
COPY go.mod ./
COPY . ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-buildid=' -o /out/api ./cmd/app

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
ARG SWITCHYARD_SOURCE_REVISION=unknown
LABEL org.opencontainers.image.source="https://git.subcult.tv/subculture-collective/switchyard" \
      org.opencontainers.image.revision="${SWITCHYARD_SOURCE_REVISION}"
WORKDIR /app
RUN apk add --no-cache git=2.49.1-r0
COPY --from=build /out/api /app/api
EXPOSE 8080
CMD ["/app/api"]

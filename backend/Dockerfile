FROM golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS build
WORKDIR /src
COPY go.mod ./
COPY . ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-buildid=' -o /out/api ./cmd/app
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-buildid=' -o /out/verifier ./cmd/verifier

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce AS runtime
ARG SWITCHYARD_SOURCE_REVISION=unknown
LABEL org.opencontainers.image.source="https://git.subcult.tv/subculture-collective/switchyard" \
      org.opencontainers.image.revision="${SWITCHYARD_SOURCE_REVISION}"
WORKDIR /app
RUN apk add --no-cache git=2.49.1-r0 nodejs=22.23.2-r0 npm=11.6.4-r0
RUN addgroup -g 1000 switchyard && adduser -D -H -u 1000 -G switchyard switchyard
USER 1000:1000

FROM runtime AS verifier
USER root
RUN apk add --no-cache bash=5.2.37-r0 python3=3.12.14-r0 make=4.4.1-r3 cargo=1.87.0-r1 \
    && npm install --global --ignore-scripts pnpm@10.33.0 \
    && npm cache clean --force
COPY --from=build /usr/local/go /usr/local/go
COPY --from=build /out/verifier /app/verifier
ENV PATH="/usr/local/go/bin:${PATH}"
USER 1000:1000
EXPOSE 8090
CMD ["/app/verifier"]

FROM runtime AS api
COPY --from=build /out/api /app/api
ENV SWITCHYARD_SOURCE_REVISION=${SWITCHYARD_SOURCE_REVISION}
USER 1000:1000
EXPOSE 8080
CMD ["/app/api"]

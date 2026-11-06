FROM node:24-alpine@sha256:a0b9bf06e4e6193cf7a0f58816cc935ff8c2a908f81e6f1a95432d679c54fbfd AS build
WORKDIR /src
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN corepack enable && pnpm install --frozen-lockfile
COPY . ./
ARG SWITCHYARD_SOURCE_REVISION=unknown
RUN pnpm run build && node -e 'require("fs").writeFileSync("dist/build-info.json", JSON.stringify({source_commit: process.env.SWITCHYARD_SOURCE_REVISION, repository: "subculture-collective/switchyard"}) + "\n")'

FROM nginx:1.29-alpine@sha256:5616878291a2eed594aee8db4dade5878cf7edcb475e59193904b198d9b830de
ARG SWITCHYARD_SOURCE_REVISION=unknown
LABEL org.opencontainers.image.source="https://git.subcult.tv/subculture-collective/switchyard" \
      org.opencontainers.image.revision="${SWITCHYARD_SOURCE_REVISION}"
COPY --from=build /src/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/templates/default.conf.template
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1/healthz && test -s /usr/share/nginx/html/build-info.json
EXPOSE 80

FROM golang@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6 AS go_toolchain
FROM docker.gitea.com/runner-images@sha256:58ea92624c7c09582e05594d95488331045053d3a3f34cf09649f2a32313a614

COPY --from=go_toolchain /usr/local/go /usr/local/go

ENV GOROOT=/usr/local/go
ENV PATH=/usr/local/go/bin:${PATH}

RUN test "$(go version | awk '{print $3}')" = "go1.26.6" \
 && test "$(node -p 'process.versions.node.split(".")[0]')" = "24" \
 && command -v corepack >/dev/null \
 && docker --version >/dev/null \
 && docker compose version >/dev/null \
 && docker buildx version >/dev/null \
 && make --version >/dev/null

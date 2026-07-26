FROM golang@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 AS go_toolchain
FROM docker.gitea.com/runner-images@sha256:58ea92624c7c09582e05594d95488331045053d3a3f34cf09649f2a32313a614

COPY --from=go_toolchain /usr/local/go /usr/local/go

ENV GOROOT=/usr/local/go
ENV PATH=/usr/local/go/bin:${PATH}

RUN test "$(go version | awk '{print $3}')" = "go1.26.5" \
 && test "$(node -p 'process.versions.node.split(".")[0]')" = "24" \
 && command -v corepack >/dev/null \
 && docker --version >/dev/null \
 && docker compose version >/dev/null \
 && docker buildx version >/dev/null \
 && make --version >/dev/null

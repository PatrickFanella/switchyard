# Dependency preparation runs during an operator-owned image build, never in the verifier.
ARG VERIFIER_BASE
FROM ${VERIFIER_BASE} AS verifier_binary
FROM golang@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6 AS go_toolchain
FROM docker.gitea.com/runner-images@sha256:58ea92624c7c09582e05594d95488331045053d3a3f34cf09649f2a32313a614
COPY --from=go_toolchain /usr/local/go /usr/local/go
COPY --from=verifier_binary /app/verifier /app/verifier
ENV PATH=/usr/local/go/bin:/usr/local/bin:$PATH
ENV GOTOOLCHAIN=local
RUN npm install --global --ignore-scripts pnpm@10.33.0
COPY inputs/ /prepare/
COPY manifest.json /opt/switchyard-verification/manifest.json
RUN mkdir -p /opt/switchyard-verification/pnpm /opt/switchyard-verification/go \
 && if [ -f /prepare/web/pnpm-lock.yaml ]; then pnpm --dir /prepare/web fetch --ignore-scripts --store-dir /opt/switchyard-verification/pnpm; fi \
 && if [ -f /prepare/backend/go.mod ]; then cd /prepare/backend && GOMODCACHE=/opt/switchyard-verification/go go mod download; fi
RUN go version && node --version && pnpm --version && docker compose version && python3 --version \
 && chmod -R a+rX /opt/switchyard-verification \
 && rm -rf /prepare /root/.npm /root/.cache
ENV SWITCHYARD_VERIFIER_BUNDLE=/opt/switchyard-verification
USER 1000:1000
WORKDIR /app
CMD ["/app/verifier"]

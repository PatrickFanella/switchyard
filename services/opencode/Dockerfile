FROM node:24-alpine@sha256:a0b9bf06e4e6193cf7a0f58816cc935ff8c2a908f81e6f1a95432d679c54fbfd

ARG SWITCHYARD_SOURCE_REVISION=unknown
LABEL org.opencontainers.image.revision="${SWITCHYARD_SOURCE_REVISION}"

RUN apk add --no-cache bash=5.3.9-r1 build-base=0.5-r4 curl=8.21.0-r0 git=2.54.0-r0 go=1.26.3-r0 make=4.4.1-r4 openssh-client-default=10.3_p1-r0 su-exec=0.3-r0 wget=1.25.0-r3 \
	&& corepack enable

ENV PATH="/usr/local/bin:/opt/opencode/node_modules/.bin:${PATH}"

ARG RTK_COMMIT=5a7880d404db8364d602f2ecdc41dd790f64013f
ARG RTK_INSTALL_SHA256=d6eb73a772903e13ff34ee1be8a8b24e896ba9a978f20d2279a08b4083ea6f77

COPY package.json package-lock.json /opt/opencode/
RUN cd /opt/opencode && npm ci --omit=dev \
	&& curl -fsSL "https://raw.githubusercontent.com/rtk-ai/rtk/${RTK_COMMIT}/install.sh" -o /tmp/rtk-install.sh \
	&& echo "${RTK_INSTALL_SHA256}  /tmp/rtk-install.sh" | sha256sum -c - \
	&& sh /tmp/rtk-install.sh \
	&& rm -f /tmp/rtk-install.sh \
	&& if [ -x /root/.local/bin/rtk ]; then install -m 0755 /root/.local/bin/rtk /usr/local/bin/rtk; fi

COPY install.sh /usr/local/bin/switchyard-opencode-install
COPY entrypoint.sh /usr/local/bin/switchyard-opencode-entrypoint
COPY default-config/ /opt/switchyard-opencode/default-config/
RUN addgroup -S -g 10001 opencode \
	&& adduser -S -D -H -u 10001 -G opencode opencode \
	&& install -d -o opencode -g opencode /home/opencode/.config/opencode /home/opencode/.local/share/opencode /home/opencode/.cache \
	&& chmod 0755 /usr/local/bin/switchyard-opencode-install /usr/local/bin/switchyard-opencode-entrypoint \
	&& chmod -R a+rX /opt/switchyard-opencode/default-config

WORKDIR /workspace

ENV HOME=/home/opencode
ENV XDG_CONFIG_HOME=/home/opencode/.config
ENV XDG_DATA_HOME=/home/opencode/.local/share
ENV XDG_CACHE_HOME=/home/opencode/.cache
ENV XDG_STATE_HOME=/home/opencode/.local/state
ENV OPENCODE_SERVER_HOSTNAME=0.0.0.0
ENV OPENCODE_SERVER_PORT=4096
ENV OPENCODE_RUNTIME_UID=10001
ENV OPENCODE_RUNTIME_GID=10001

VOLUME ["/home/opencode/.config/opencode", "/home/opencode/.local/share/opencode", "/home/opencode/.local/state", "/home/opencode/.cache"]

EXPOSE 4096

ENTRYPOINT ["/usr/local/bin/switchyard-opencode-entrypoint"]
CMD ["sh", "-c", "switchyard-opencode-install && opencode serve --hostname ${OPENCODE_SERVER_HOSTNAME:-0.0.0.0} --port ${OPENCODE_SERVER_PORT:-4096}"]

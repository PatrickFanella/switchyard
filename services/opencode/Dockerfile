FROM node:24-alpine

RUN apk add --no-cache bash build-base curl git go make openssh-client wget \
	&& corepack enable

ENV PATH="/root/.local/bin:${PATH}"

ARG RTK_COMMIT=5a7880d404db8364d602f2ecdc41dd790f64013f
ARG RTK_INSTALL_SHA256=d6eb73a772903e13ff34ee1be8a8b24e896ba9a978f20d2279a08b4083ea6f77

RUN npm install -g opencode-ai@1.17.18 @tarquinen/opencode-dcp@3.1.14 @slkiser/opencode-quota@3.11.2 opencode-tasks@0.5.0 @opencode-ai/plugin@1.17.18 bun@1.3.14 \
	&& curl -fsSL "https://raw.githubusercontent.com/rtk-ai/rtk/${RTK_COMMIT}/install.sh" -o /tmp/rtk-install.sh \
	&& echo "${RTK_INSTALL_SHA256}  /tmp/rtk-install.sh" | sha256sum -c - \
	&& sh /tmp/rtk-install.sh \
	&& rm -f /tmp/rtk-install.sh \
	&& if [ -x /root/.local/bin/rtk ]; then ln -sf /root/.local/bin/rtk /usr/local/bin/rtk; fi

COPY install.sh /usr/local/bin/switchyard-opencode-install
COPY default-config/ /opt/switchyard-opencode/default-config/
RUN chmod +x /usr/local/bin/switchyard-opencode-install

WORKDIR /workspace

ENV OPENCODE_SERVER_HOSTNAME=0.0.0.0
ENV OPENCODE_SERVER_PORT=4096

EXPOSE 4096

CMD ["sh", "-c", "switchyard-opencode-install && opencode serve --hostname ${OPENCODE_SERVER_HOSTNAME:-0.0.0.0} --port ${OPENCODE_SERVER_PORT:-4096}"]

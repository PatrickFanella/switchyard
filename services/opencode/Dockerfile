FROM node:24-alpine

RUN apk add --no-cache bash build-base curl git go make openssh-client wget \
	&& corepack enable

ENV PATH="/root/.local/bin:${PATH}"

RUN npm install -g opencode-ai@latest @tarquinen/opencode-dcp@latest @slkiser/opencode-quota opencode-tasks @opencode-ai/plugin bun \
	&& (curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh | sh || true) \
	&& if [ -x /root/.local/bin/rtk ]; then ln -sf /root/.local/bin/rtk /usr/local/bin/rtk; fi

COPY install.sh /usr/local/bin/switchyard-opencode-install
COPY default-config/ /opt/switchyard-opencode/default-config/
RUN chmod +x /usr/local/bin/switchyard-opencode-install

WORKDIR /workspace

ENV OPENCODE_SERVER_HOSTNAME=0.0.0.0
ENV OPENCODE_SERVER_PORT=4096

EXPOSE 4096

CMD ["sh", "-c", "switchyard-opencode-install && opencode serve --hostname ${OPENCODE_SERVER_HOSTNAME:-0.0.0.0} --port ${OPENCODE_SERVER_PORT:-4096}"]

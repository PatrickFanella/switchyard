FROM node:24-alpine

RUN apk add --no-cache bash build-base curl git go make openssh-client wget \
	&& corepack enable

RUN npm install -g opencode-ai

WORKDIR /workspace

ENV OPENCODE_SERVER_HOSTNAME=0.0.0.0
ENV OPENCODE_SERVER_PORT=4096

EXPOSE 4096

CMD ["sh", "-c", "opencode serve --hostname ${OPENCODE_SERVER_HOSTNAME:-0.0.0.0} --port ${OPENCODE_SERVER_PORT:-4096}"]

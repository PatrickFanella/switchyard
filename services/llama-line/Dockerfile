FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN go build -o /out/llama-line ./cmd/app

FROM alpine:3.22

WORKDIR /app
COPY --from=build /out/llama-line /app/llama-line
COPY config.switchyard.yaml /app/config.yaml

EXPOSE 11434
CMD ["/app/llama-line", "--config", "/app/config.yaml"]

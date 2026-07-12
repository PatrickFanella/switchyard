FROM golang:1.26.5-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . ./
RUN go build -o /out/api ./cmd/app

FROM alpine:3.22
WORKDIR /app
RUN apk add --no-cache git
COPY --from=build /out/api /app/api
EXPOSE 8080
CMD ["/app/api"]

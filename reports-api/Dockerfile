FROM golang:1.23-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY *.go ./

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/reports-api .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates \
    && addgroup -S reports-api \
    && adduser -S -G reports-api reports-api

COPY --from=build /out/reports-api /usr/local/bin/reports-api

USER reports-api
EXPOSE 8000

ENTRYPOINT ["/usr/local/bin/reports-api"]


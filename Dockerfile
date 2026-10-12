# Always-on mode for a home server / old laptop / Raspberry Pi:
#   docker compose up -d
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /aptbot ./cmd/aptbot

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /aptbot /usr/local/bin/aptbot
WORKDIR /app
ENTRYPOINT ["aptbot", "-config", "/app/config.json"]

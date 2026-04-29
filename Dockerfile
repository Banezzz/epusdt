FROM golang:1.25.9-alpine AS builder

RUN apk add --no-cache --update git build-base
ENV CGO_ENABLED=0

WORKDIR /app

COPY . /app

WORKDIR /app/src
RUN go mod download
RUN go build -o /app/epusdt .

FROM alpine:3.22 AS runner
ENV TZ=Asia/Shanghai
RUN apk --no-cache add ca-certificates tzdata \
    && addgroup -S epusdt \
    && adduser -S -D -H -G epusdt epusdt
ARG API_RATE_URL=""

WORKDIR /app
COPY --from=builder --chown=epusdt:epusdt /app/src/www /app/www
COPY --from=builder --chown=epusdt:epusdt /app/src/.env.example /app/.env
RUN if [ -n "$API_RATE_URL" ]; then \
      sed -i "s|^api_rate_url=.*$|api_rate_url=${API_RATE_URL}|" /app/.env; \
    fi \
    && mkdir -p /app/conf /app/data/runtime /app/runtime /app/runtime/logs /app/static \
    && chown -R epusdt:epusdt /app
COPY --from=builder --chown=epusdt:epusdt /app/epusdt .

VOLUME /app/conf
USER epusdt
ENTRYPOINT ["./epusdt", "http", "start"]

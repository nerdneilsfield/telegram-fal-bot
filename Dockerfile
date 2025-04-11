FROM alpine:latest

COPY telegram-fal-bot /app/telegram-fal-bot
COPY config.toml /app/config.toml

ENTRYPOINT ["/app/telegram-fal-bot", "start", "/app/config.toml"]
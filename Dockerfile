FROM golang:1.24-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/flowbridge .

FROM debian:bookworm-slim

WORKDIR /app
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -r -u 10001 -g root flowbridge \
    && mkdir -p /app/data \
    && chown -R flowbridge:root /app

COPY --from=builder /out/flowbridge /app/flowbridge
COPY config.json /app/config.json

USER flowbridge
EXPOSE 8080

CMD ["/app/flowbridge"]

# viagem — micro site do roteiro Ibérico (Coolify)
# Imagem única: binário Go com o site embutido (go:embed).

FROM golang:1.26-alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o viagem .

FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata && \
    ln -sf /usr/share/zoneinfo/America/Sao_Paulo /etc/localtime

RUN addgroup -g 1001 -S appgroup && \
    adduser  -u 1001 -S appuser -G appgroup

WORKDIR /app
COPY --from=builder /app/viagem .

RUN mkdir -p /data && chown -R appuser:appgroup /data /app
USER appuser

ENV ADDR=0.0.0.0:8080
ENV DB_PATH=/data/trip.json
EXPOSE 8080

ENTRYPOINT ["./viagem"]

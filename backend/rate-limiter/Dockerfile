FROM golang:1.21-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/rate-limiter .

FROM alpine:latest

RUN apk --no-cache add ca-certificates wget

WORKDIR /app
COPY --from=builder /out/rate-limiter ./rate-limiter

EXPOSE 8080

CMD ["./rate-limiter"]

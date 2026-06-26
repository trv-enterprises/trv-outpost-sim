FROM golang:1.21-alpine AS builder

WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY *.go ./

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /counter-writer .

# Final stage - minimal image
FROM alpine:3.18

RUN apk --no-cache add ca-certificates wget

RUN adduser -D -u 1000 counterwriter
USER counterwriter

COPY --from=builder /counter-writer /usr/local/bin/

CMD ["counter-writer"]

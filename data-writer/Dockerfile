FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod ./
RUN go mod download

# Copy source code
COPY *.go ./

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /data-writer .

# Final stage - minimal image
FROM alpine:3.18

RUN apk --no-cache add ca-certificates wget

# Create non-root user
RUN adduser -D -u 1000 datawriter
USER datawriter

COPY --from=builder /data-writer /usr/local/bin/

CMD ["data-writer"]

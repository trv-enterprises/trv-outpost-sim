FROM golang:1.21-alpine AS builder

WORKDIR /app

# No external dependencies — copy go.mod only (no go.sum needed).
COPY go.mod ./

# Copy source code
COPY *.go ./

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /sensor-readings-writer .

# Final stage - minimal image
FROM alpine:3.18

RUN apk --no-cache add ca-certificates wget

# Create non-root user
RUN adduser -D -u 1000 srwriter
USER srwriter

COPY --from=builder /sensor-readings-writer /usr/local/bin/

CMD ["sensor-readings-writer"]

FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source + embedded dataset (data/marx-geo.csv.gz is go:embed'd into the binary)
COPY *.go ./
COPY data ./data

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /traffic-writer .

# Final stage - minimal image
FROM alpine:3.18

RUN apk --no-cache add ca-certificates wget

# Create non-root user
RUN adduser -D -u 1000 trafficsim
USER trafficsim

COPY --from=builder /traffic-writer /usr/local/bin/

CMD ["traffic-writer"]

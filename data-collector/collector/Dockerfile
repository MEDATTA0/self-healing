# Dockerfile inspired by https://docs.docker.com/guides/go-prometheus-monitoring/containerize/

FROM golang:1.24-alpine AS builder

RUN apk update && \
    apk install -y curl 

# Set environment variables
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

# Set working directory inside the container
WORKDIR /build

# Copy go.mod and go.sum files for dependency installation
COPY go.mod go.sum /build/

# Download dependencies
RUN go mod download

# Copy the entire application source
COPY ./ /build/

# Build the Go binary
RUN go build -o /app .

# Final lightweight stage
FROM alpine:3.21 AS final

# Copy the compiled binary from the builder stage
COPY --from=builder /app /go-app/app

# Expose the application's port
EXPOSE 1324

# Run the application
CMD [ "/go-app/app" ]
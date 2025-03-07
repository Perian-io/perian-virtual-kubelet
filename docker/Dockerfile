# Stage 1: Build the application with Go
FROM golang:1.23 AS build-stage

WORKDIR /app

# Copy go module files and install dependencies (use cache to avoid repeated downloads)
COPY go.mod go.sum ./

RUN go mod tidy

# Copy the source code
COPY . .

# Build the binary
ARG VERSION
RUN CGO_ENABLED=0 GOOS=linux go build -o ./bin/vk ./src/cmd/main.go

# Stage 2: Prepare the runtime environment
FROM ubuntu:22.04 AS build-release-stage

# Install kubectl and dependencies for the app to run
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Copy the compiled binary from the build stage
COPY --from=build-stage /app/bin/vk /vk

# Copy kubectl from the kubectl base image
COPY --from=bitnami/kubectl:1.27.14 /opt/bitnami/kubectl/bin/kubectl /usr/local/bin/

# Final image is lean, remove unnecessary tools
FROM scratch AS final-stage

# Copy necessary files from build-release-stage
COPY --from=build-release-stage /vk /vk
COPY --from=build-release-stage /usr/local/bin/kubectl /usr/local/bin/kubectl

# Set the entry point for the container
ENTRYPOINT ["/vk"]

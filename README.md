# Gateway Microservice

API Gateway microservice for the MiniSource platform. Powered by Traefik and Go backend service for routing, rate limiting, access control, and centralized entry point.

## Features

- **Traefik Reverse Proxy & Load Balancing**
- **Go Backend API Gateway Service**
- **JWT & Service Token Authentication Forwarding**
- **CORS & Security Headers**
- **Rate Limiting & Request Throttling**
- **Prometheus Metrics & Health Checks**
- **Automated CI/CD & Docker Publishing**

## Docker Image

Published to Docker Hub:
- `minisource/gateway:v1.0.1`
- `minisource/gateway:latest`

```bash
docker pull minisource/gateway:v1.0.1
```

## Structure

```
gateway/
├── backend/              # Go API Gateway Service (Go 1.24)
│   ├── cmd/
│   ├── internal/
│   └── Dockerfile
├── Traefik/              # Traefik configuration files
├── docker-compose.yml
├── go.mod
└── .github/workflows/    # CI/CD workflows
```

## Quick Start

### Running with Docker Compose

```bash
cd gateway
docker compose up -d
```

### Local Development (Backend)

```bash
cd gateway/backend
go run ./cmd/server
```

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `PORT` | Gateway HTTP Port | `8000` |
| `ENV` | Environment (`development`/`production`) | `development` |
| `AUTH_SERVICE_URL` | Auth Microservice URL | `http://auth-backend:9001` |
| `NOTIFIER_SERVICE_URL` | Notifier Microservice URL | `http://notifier-backend:9002` |

## CI/CD Pipeline

Automated by GitHub Actions:
- **Build & Test**: `go vet` and test execution on every commit.
- **Docker Release**: Builds and pushes Docker image to `minisource/gateway` on tag push (`v*`).

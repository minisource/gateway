# Minisource Gateway

API Gateway for the Minisource microservices ecosystem. Provides unified entry point with authentication, rate limiting, circuit breaking, and request routing.

## Features

- 🔀 **Reverse Proxy** - Route requests to backend services
- 🔐 **JWT Authentication** - Validate and forward authentication tokens
- ⚡ **Rate Limiting** - Redis-backed rate limiting per client
- 🔌 **Circuit Breaker** - Automatic failover and recovery
- 📊 **Tracing** - OpenTelemetry distributed tracing
- 🏥 **Health Checks** - Monitor backend service health
- 🛡️ **Security Headers** - CORS, CSP, and security middleware

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    API Gateway (:8080)                   │
├─────────────────────────────────────────────────────────┤
│  ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐    │
│  │  Auth   │  │  Rate   │  │ Circuit │  │ Tracing │    │
│  │ Middle  │  │ Limiter │  │ Breaker │  │         │    │
│  └────┬────┘  └────┬────┘  └────┬────┘  └────┬────┘    │
│       └────────────┴────────────┴────────────┘          │
│                         │                                │
│                   ┌─────▼─────┐                         │
│                   │   Proxy   │                         │
│                   │  Router   │                         │
│                   └─────┬─────┘                         │
└─────────────────────────┼───────────────────────────────┘
                          │
       ┌──────────────────┼──────────────────┐
       ▼                  ▼                  ▼
┌─────────────┐   ┌─────────────┐   ┌─────────────┐
│    Auth     │   │  Notifier   │   │   Other     │
│   :9001     │   │   :9002     │   │  Services   │
└─────────────┘   └─────────────┘   └─────────────┘
```

## Quick Start

### Prerequisites

- Go 1.24+
- Redis 7+
- Docker & Docker Compose (optional)

### Development

```bash
# Clone repository
git clone https://github.com/minisource/gateway.git
cd gateway

# Copy environment file
cp .env.example .env

# Run with Docker Compose
make docker-up

# Or run locally
make run
```

### Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `SERVER_PORT` | Gateway port | `8080` |
| `SERVER_HOST` | Bind address | `0.0.0.0` |
| `AUTH_SERVICE_URL` | Auth service URL | `http://localhost:9001` |
| `NOTIFIER_SERVICE_URL` | Notifier service URL | `http://localhost:9002` |
| `REDIS_HOST` | Redis host | `localhost` |
| `REDIS_PORT` | Redis port | `6379` |
| `JWT_SECRET` | JWT signing secret | Required |
| `RATE_LIMIT_ENABLED` | Enable rate limiting | `true` |
| `RATE_LIMIT_RPS` | Requests per second | `100` |
| `CIRCUIT_ENABLED` | Enable circuit breaker | `true` |
| `TRACING_ENABLED` | Enable OpenTelemetry | `true` |

## API Routes

### Authentication Routes (Proxied to Auth Service)

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/auth/register` | User registration |
| POST | `/api/v1/auth/login` | User login |
| POST | `/api/v1/auth/refresh` | Refresh token |
| POST | `/api/v1/auth/logout` | User logout |
| GET | `/api/v1/auth/me` | Get current user |

### Notification Routes (Proxied to Notifier Service)

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/notifications/send` | Send notification |
| GET | `/api/v1/notifications` | List notifications |

### Gateway Routes

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Gateway health check |
| GET | `/metrics` | Prometheus metrics |

## Makefile Commands

```bash
make build         # Build binary
make run           # Run locally
make test          # Run tests
make lint          # Run linter
make docker-build  # Build Docker image
make docker-up     # Start with docker-compose
make docker-down   # Stop containers
```

## Adding New Routes

1. Add service configuration in `config/config.go`
2. Create handler in `internal/handler/`
3. Add proxy configuration in `internal/proxy/`
4. Register routes in `internal/router/router.go`

## Middleware Stack

1. **Recovery** - Panic recovery
2. **Request ID** - Add unique request ID
3. **Logger** - Request logging
4. **CORS** - Cross-origin resource sharing
5. **Rate Limiter** - Request rate limiting
6. **Auth** - JWT validation (protected routes)
7. **Circuit Breaker** - Failure isolation

## Docker

Images are published to [Docker Hub](https://hub.docker.com/orgs/minisource/repositories) on every successful build to `main`.

| Image | Tags |
|-------|------|
| `minisource/gateway` | `latest`, commit SHA |

```bash
# Production (pre-built image)
export TAG=latest
docker compose -f docker-compose.prod.yml up -d

# Local build
docker build -t minisource/gateway .
docker run -p 8080:8080 --env-file .env minisource/gateway
```

### GitHub Actions secrets

- `DOCKERHUB_USERNAME` — Docker Hub username
- `DOCKERHUB_TOKEN` — Docker Hub access token

## Environment Files

- `.env.example` - Template configuration
- `.env` - Local development (git ignored)
- `.env.production` - Production settings

## Project Structure

```
gateway/
├── cmd/
│   └── main.go              # Entry point
├── config/
│   └── config.go            # Configuration loading
├── internal/
│   ├── handler/             # Request handlers
│   ├── middleware/          # Custom middleware
│   ├── proxy/               # Reverse proxy logic
│   └── router/              # Route definitions
├── docker-compose.yml       # Base compose
├── docker-compose.dev.yml   # Development compose
├── docker-compose.prod.yml  # Production compose
├── Dockerfile               # Container build
└── Makefile                 # Build commands
```

## License

MIT
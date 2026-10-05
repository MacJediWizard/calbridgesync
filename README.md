# CalBridgeSync

A production-ready Go application for bidirectional CalDAV calendar synchronization with OIDC authentication.

## Features

- **CalDAV Synchronization**: Sync calendars between any CalDAV-compatible servers
- **Guarded Full Sync**: Each cycle compares the full calendars, skips unchanged events by ETag, and runs deletions through safety guards
- **OIDC Authentication**: Secure single sign-on via OpenID Connect
- **Encrypted Credentials**: AES-256-GCM encryption for stored credentials
- **Background Scheduling**: Configurable automatic sync intervals
- **Web Dashboard**: HTMX + Tailwind CSS interface for management
- **Health Monitoring**: Kubernetes-ready health endpoints
- **Docker Ready**: Multi-stage build with security best practices

## Requirements

- Go 1.22 or later
- SQLite (pure Go implementation, no CGO required)
- OIDC provider (Keycloak, Auth0, Okta, etc.)

## Quick Start

### Environment Variables

Create a `.env` file based on `.env.example`:

```bash
# Server
PORT=8080
BASE_URL=https://calbridgesync.example.com
ENVIRONMENT=production

# OIDC Authentication
OIDC_ISSUER=https://auth.example.com/realms/main
OIDC_CLIENT_ID=calbridgesync
OIDC_CLIENT_SECRET=your-client-secret
OIDC_REDIRECT_URL=https://calbridgesync.example.com/auth/callback

# Security (generate with: openssl rand -hex 32)
ENCRYPTION_KEY=your-64-character-hex-encryption-key
SESSION_SECRET=your-session-secret-min-32-chars

# CalDAV
DEFAULT_DEST_URL=https://caldav.example.com/calendars/

# Database
DATABASE_PATH=./data/calbridgesync.db

# Rate Limiting
RATE_LIMIT_RPS=10
RATE_LIMIT_BURST=20

# Sync Intervals (seconds)
MIN_SYNC_INTERVAL=30
MAX_SYNC_INTERVAL=3600
```

### Running with Docker

```bash
# Build and run
docker-compose up -d

# View logs
docker-compose logs -f calbridgesync
```

### Running Locally

```bash
# Install dependencies
go mod download

# Run the application
go run ./cmd/calbridgesync

# Or build and run
go build -o calbridgesync ./cmd/calbridgesync
./calbridgesync
```

## API Endpoints

### Health Checks

| Endpoint | Description |
|----------|-------------|
| `GET /health` | Full health report (JSON) |
| `GET /healthz` | Liveness probe |
| `GET /ready` | Readiness probe |

### Authentication

| Endpoint | Description |
|----------|-------------|
| `GET /auth/login` | Login page |
| `POST /auth/login` | Initiate OIDC flow |
| `GET /auth/callback` | OIDC callback |
| `POST /auth/logout` | Logout |

### Dashboard (Protected)

| Endpoint | Description |
|----------|-------------|
| `GET /` | Dashboard |
| `GET /sources` | List sources |
| `GET /sources/add` | Add source form |
| `POST /sources/add` | Create source |
| `GET /sources/:id/edit` | Edit source form |
| `POST /sources/:id` | Update source |
| `DELETE /sources/:id` | Delete source |
| `POST /sources/:id/sync` | Trigger sync |
| `POST /sources/:id/toggle` | Enable/disable |
| `GET /sources/:id/logs` | View sync logs |

## Security Features

- **HTTPS Required**: Production mode enforces HTTPS for all URLs
- **Private IP Blocking**: Prevents SSRF attacks
- **TLS 1.2 Minimum**: Modern TLS requirements
- **Security Headers**: CSP, X-Frame-Options, X-XSS-Protection
- **Rate Limiting**: Configurable request rate limiting
- **CSRF Protection**: Token-based CSRF protection
- **Session Security**: HttpOnly, Secure, SameSite cookies
- **Credential Encryption**: AES-256-GCM for stored passwords

## Development

### Prerequisites

```bash
# Install golangci-lint
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

### Commands

```bash
# Build
go build ./...

# Test
go test -v ./...

# Lint
golangci-lint run ./...

# Vet
go vet ./...
```

### Generate Encryption Key

```bash
openssl rand -hex 32
```

## Architecture

```
calbridgesync/
├── cmd/calbridgesync/         # Main entry point
├── internal/
│   ├── auth/              # OIDC + session management
│   ├── caldav/            # CalDAV client + sync engine
│   ├── config/            # Configuration loading
│   ├── crypto/            # AES-256-GCM encryption
│   ├── db/                # SQLite database layer
│   ├── health/            # Health check endpoints
│   ├── scheduler/         # Background job scheduler
│   ├── validator/         # URL + OIDC validation
│   └── web/               # HTTP handlers + templates
├── scripts/               # Docker entrypoint
├── Dockerfile             # Multi-stage Docker build
├── docker-compose.yml     # Docker Compose config
└── .golangci.yml          # Linter configuration
```

## License

MIT License

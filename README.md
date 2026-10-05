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
# Reverse proxy IPs/CIDRs allowed to set X-Forwarded-For (comma-separated).
# Empty = trust none. Behind a reverse proxy, set this or every user
# shares one rate-limit bucket and audit logs show the proxy IP.
TRUSTED_PROXIES=172.17.0.1

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

## Google Calendar Sources

Google sources sync over Google's CalDAV API using OAuth 2.0. Each source
uses a Google Cloud OAuth client that you create; its client ID and secret
are entered in the add-source form. The secret and the refresh token are
stored encrypted.

### Google Cloud setup

1. In the [Google Cloud Console](https://console.cloud.google.com/), create
   or select a project.
2. Under **APIs & Services > Library**, enable the **CalDAV API**. Without
   it, every sync fails even when OAuth succeeds.
3. Configure the **OAuth consent screen** and add these scopes:
   - `https://www.googleapis.com/auth/calendar` (CalDAV read/write)
   - `https://www.googleapis.com/auth/userinfo.email` (used to build the
     account's CalDAV URL)
4. **Set the consent screen's publishing status to "In production".** While
   an app is in **Testing**, Google expires its refresh tokens after
   **7 days**. Syncs then fail with `oauth2: "invalid_grant"` and the source
   has to be reconnected. An unverified app that is in production shows an
   "unverified app" warning during consent, which you can click through for
   your own account. Its refresh tokens do not expire on a timer.
5. Under **Credentials**, create an **OAuth client ID** of type **Web
   application**. Add this authorized redirect URI:
   `<BASE_URL>/auth/oauth/google/callback`, or the value of
   `GOOGLE_OAUTH_REDIRECT_URL` if you set that to override it.

CalBridgeSync always requests offline access with `prompt=consent`, so Google
issues a refresh token on every authorization. If Google returns no refresh
token, the flow fails with an error instead of saving a source that cannot
sync.

### Reconnecting an expired or revoked Google account

A refresh token stops working when access is revoked at
<https://myaccount.google.com/permissions>, when the password changes, after
the 7-day Testing expiry, or when the token goes unused for 6 months. Once
that happens, the source shows "Google authorization expired or was revoked"
in the web UI and the sources list shows a **Reconnect** link. After 3
consecutive authentication failures, the credential-expiry alert is sent
through the configured alert channels.

To fix it, open the source (**Edit**) and click **Reconnect Google account**,
then sign in with the **same** Google account. Only the stored refresh token
is replaced: the source ID, settings and sync history are kept, and a sync
starts right away. A different Google account is rejected, because it would
point the existing source at another calendar.

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

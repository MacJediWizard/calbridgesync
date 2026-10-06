# CalBridgeSync

A production-ready Go application for bidirectional CalDAV calendar synchronization with OIDC authentication.

## Features

- **CalDAV Synchronization**: Sync calendars between any CalDAV-compatible servers
- **Guarded Full Sync**: Each cycle compares the full calendars, skips unchanged events by ETag, and runs deletions through safety guards
- **OIDC Authentication**: Secure single sign-on via OpenID Connect
- **Encrypted Credentials**: AES-256-GCM encryption for stored credentials
- **Background Scheduling**: Configurable automatic sync intervals
- **Web Dashboard**: React + Tailwind CSS single-page app for management
- **Health Monitoring**: Kubernetes-ready health endpoints
- **Docker Ready**: Multi-stage build with security best practices

## Requirements

- Go 1.26 or later
- SQLite (pure Go implementation, no CGO required)
- OIDC provider (Keycloak, Auth0, Okta, etc.)

## Quick Start

### Environment Variables

Create a `.env` file based on `.env.example`:

```bash
# Server
PORT=8080
BASE_URL=https://calbridgesync.example.com
# Must be "production" or "development"; anything else fails at startup.
ENVIRONMENT=production
# Required in production: browser origins allowed by the CSRF check (comma-separated).
ALLOWED_ORIGINS=https://calbridgesync.example.com
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

# CalDAV (in production, http:// is allowed only for private or loopback hosts)
DEFAULT_DEST_URL=https://caldav.example.com/calendars/
# Per-request HTTP timeout for CalDAV and ICS calls, in seconds (default 300)
CALDAV_REQUEST_TIMEOUT=300

# Database
DATABASE_PATH=./data/calbridgesync.db

# Sync Intervals (seconds)
MIN_SYNC_INTERVAL=30
MAX_SYNC_INTERVAL=86400
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
| `GET /auth/login`, `POST /auth/login` | Start the OIDC flow (redirects to the provider) |
| `GET /auth/callback` | OIDC callback |
| `POST /auth/logout` | Logout |
| `GET /auth/oauth/google/callback` | Google OAuth redirect URI for Google sources |

### JSON API and web UI

The web UI is a React single-page app. `internal/web/routes.go` serves it
for every path that isn't under `/api`, `/auth` or a health endpoint. The
UI talks to a JSON API under `/api`. Apart from `GET /api/auth/status`,
`GET /api/version` and `POST /api/auth/logout`, every API route requires a
session, and requests that change state must pass the Origin check
described below.

| Endpoint | Description |
|----------|-------------|
| `GET /api/sources`, `POST /api/sources` | List or create sources |
| `GET/PUT/DELETE /api/sources/:id` | Read, update or delete a source |
| `POST /api/sources/:id/sync` | Trigger a sync |
| `POST /api/sources/:id/toggle` | Enable or disable a source |
| `GET /api/sources/:id/logs`, `GET /api/sources/:id/stats` | Sync logs and stats |
| `POST /api/sources/google/prepare`, `POST /api/sources/:id/google/reconnect` | Start or redo Google authorization |
| `POST /api/calendars/discover` | Discover calendars on a CalDAV server |
| `GET /api/dashboard/stats`, `GET /api/dashboard/sync-history` | Dashboard data |
| `GET/PUT /api/settings/alerts`, `POST /api/settings/alerts/test-webhook` | Alert preferences |
| `GET /api/export/calendars` | Export the user's calendars as ICS |

See `internal/web/routes.go` for the full list.

## Security Features

- **HTTPS for configured URLs**: Startup validation requires `https://` for
  `OIDC_ISSUER` always, and for `BASE_URL` and `OIDC_REDIRECT_URL` in
  production. In production, `DEFAULT_DEST_URL` may use `http://` only for
  private or loopback hosts. Webhook URLs must be `https://`. ICS feed URLs
  may be `http://` unless `STRICT_ICS_HTTPS=true` is set. CalDAV source and
  destination URLs entered in the UI are not restricted to HTTPS. The server
  listens on plain HTTP, so put a TLS-terminating reverse proxy in front of
  it.
- **SSRF protection**: Webhook URLs are rejected if they point to loopback,
  private, link-local, unspecified or CGNAT addresses. They are checked when
  saved and again when the connection is made. CalDAV and ICS connections
  refuse loopback, unspecified and link-local addresses (link-local covers
  cloud metadata endpoints) when the connection is made. Private LAN ranges
  are allowed for them so that LAN servers such as SOGo, Nextcloud and
  Radicale work.
- **TLS 1.2 minimum** on outbound CalDAV, ICS, webhook and SMTP connections.
- **Security headers**: Every response gets:
  - a Content-Security-Policy that allows no inline scripts and no script CDNs
  - `X-Frame-Options: DENY`
  - `X-Content-Type-Options: nosniff`
  - `Referrer-Policy`
  - `Permissions-Policy`
  - `X-XSS-Protection`

  HSTS is added when the request arrived over HTTPS.
- **Rate limiting**: The limits are fixed in code and applied per client IP:
  5 req/s (burst 10) on `/auth`, 30 req/s (burst 60) on the API, and 2 req/s
  (burst 5) on endpoints that make outbound network calls. Set
  `TRUSTED_PROXIES` so that client IPs are read correctly behind a proxy.
- **CSRF protection**: There are no CSRF tokens. The Origin header (or the
  Referer when Origin is missing) of state-changing `/api` requests is
  checked against `ALLOWED_ORIGINS`. Session cookies are `SameSite=Lax`, and
  the OIDC and Google OAuth flows check a `state` parameter.
- **Session cookies**: `HttpOnly` and `SameSite=Lax`, plus `Secure` in
  production.
- **Credential encryption**: Stored source and destination passwords, Google
  OAuth client secrets and refresh tokens are encrypted with AES-256-GCM.

## Development

### Prerequisites

```bash
# Install golangci-lint
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```

### Commands

```bash
# Build
go build ./...

# Test (CI also runs with -race)
go test -race ./...

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
│   ├── activity/          # In-memory sync activity tracking
│   ├── auth/              # OIDC + session management
│   ├── backup/            # Database backups
│   ├── caldav/            # CalDAV/ICS clients + sync engine
│   ├── config/            # Configuration loading and validation
│   ├── crypto/            # AES-256-GCM encryption
│   ├── db/                # SQLite database layer
│   ├── health/            # Health check endpoints
│   ├── notify/            # Email and webhook alerts
│   ├── scheduler/         # Background job scheduler
│   ├── validator/         # URL validation
│   ├── version/           # Build version
│   └── web/               # HTTP handlers, JSON API, error template
├── web/                   # React + Vite single-page app
├── scripts/               # Docker entrypoint, backup script
├── Dockerfile             # Multi-stage Docker build
├── docker-compose.yml     # Docker Compose config
└── .golangci.yml          # Linter configuration
```

## License

MIT License

package validator

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrInvalidURL        = errors.New("invalid URL format")
	ErrHTTPSRequired     = errors.New("HTTPS is required")
	ErrPrivateIP         = errors.New("private IP addresses are not allowed")
	ErrTooManyRedirects  = errors.New("too many redirects")
	ErrConnectionFailed  = errors.New("connection failed")
	ErrInvalidOIDCIssuer = errors.New("invalid OIDC issuer")
	ErrInvalidCalDAV     = errors.New("invalid CalDAV endpoint")
)

const (
	maxRedirects   = 3
	defaultTimeout = 10 * time.Second
	minTLSVersion  = tls.VersionTLS12
)

// Validator provides URL and endpoint validation functionality.
type Validator struct {
	client          *http.Client
	allowPrivateIPs bool
}

// Option configures a Validator.
type Option func(*Validator)

// WithAllowPrivateIPs allows connections to private IP addresses.
// This is useful for Docker internal networking.
func WithAllowPrivateIPs() Option {
	return func(v *Validator) {
		v.allowPrivateIPs = true
	}
}

// New creates a new Validator with the given options.
func New(opts ...Option) *Validator {
	v := &Validator{
		allowPrivateIPs: false,
	}

	for _, opt := range opts {
		opt(v)
	}

	v.client = v.createHTTPClient()
	return v
}

func (v *Validator) createHTTPClient() *http.Client {
	redirectCount := 0

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: minTLSVersion,
		},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return v.dialWithIPCheck(ctx, network, addr)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &http.Client{
		Timeout:   defaultTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			redirectCount++
			if redirectCount > maxRedirects {
				return ErrTooManyRedirects
			}
			return nil
		},
	}
}

func (v *Validator) dialWithIPCheck(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	// Allow Docker host networking (typically port 20000)
	if v.allowPrivateIPs && port == "20000" {
		dialer := &net.Dialer{
			Timeout:   defaultTimeout,
			KeepAlive: 30 * time.Second,
		}
		return dialer.DialContext(ctx, network, addr)
	}

	// Resolve the hostname to check IP addresses
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("DNS resolution failed: %w", err)
	}

	for _, ip := range ips {
		if !v.allowPrivateIPs && isPrivateIP(ip) {
			return nil, ErrPrivateIP
		}
	}

	dialer := &net.Dialer{
		Timeout:   defaultTimeout,
		KeepAlive: 30 * time.Second,
	}
	return dialer.DialContext(ctx, network, addr)
}

// isPrivateIP checks if an IP address is private or reserved.
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}

	// Check for loopback
	if ip.IsLoopback() {
		return true
	}

	// Check for private networks
	if ip.IsPrivate() {
		return true
	}

	// Check for link-local
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	// Check for unspecified (0.0.0.0 or ::)
	if ip.IsUnspecified() {
		return true
	}

	return false
}

// ValidateURL validates a URL string.
// If requireHTTPS is true, only HTTPS URLs are accepted.
func (v *Validator) ValidateURL(rawURL string, requireHTTPS bool) error {
	if rawURL == "" {
		return ErrInvalidURL
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: parse error: %w", ErrInvalidURL, err)
	}

	if parsed.Host == "" {
		return fmt.Errorf("%w: missing host", ErrInvalidURL)
	}

	if requireHTTPS && parsed.Scheme != "https" {
		return ErrHTTPSRequired
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%w: scheme must be http or https", ErrInvalidURL)
	}

	return nil
}

// ValidateOIDCIssuer validates an OIDC issuer URL by checking its discovery endpoint.
func (v *Validator) ValidateOIDCIssuer(ctx context.Context, issuerURL string) error {
	if err := v.ValidateURL(issuerURL, true); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOIDCIssuer, err)
	}

	// Normalize the issuer URL
	issuerURL = strings.TrimSuffix(issuerURL, "/")

	// Check the .well-known/openid-configuration endpoint
	discoveryURL := issuerURL + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return fmt.Errorf("%w: failed to create request: %w", ErrInvalidOIDCIssuer, err)
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: discovery endpoint returned status %d", ErrInvalidOIDCIssuer, resp.StatusCode)
	}

	return nil
}

// ValidateCalDAVEndpoint validates a CalDAV endpoint by checking its OPTIONS response.
func (v *Validator) ValidateCalDAVEndpoint(ctx context.Context, endpointURL string) error {
	if err := v.ValidateURL(endpointURL, true); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCalDAV, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, endpointURL, nil)
	if err != nil {
		return fmt.Errorf("%w: failed to create request: %w", ErrInvalidCalDAV, err)
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	defer resp.Body.Close()

	// CalDAV endpoints should return 200 OK or 204 No Content for OPTIONS
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("%w: OPTIONS returned status %d", ErrInvalidCalDAV, resp.StatusCode)
	}

	// Check for DAV header indicating WebDAV/CalDAV support
	davHeader := resp.Header.Get("DAV")
	if davHeader == "" {
		return fmt.Errorf("%w: missing DAV header", ErrInvalidCalDAV)
	}

	return nil
}

// TestConnection tests if a URL is reachable.
func (v *Validator) TestConnection(ctx context.Context, rawURL string) error {
	if err := v.ValidateURL(rawURL, false); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return fmt.Errorf("%w: failed to create request: %w", ErrConnectionFailed, err)
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	defer resp.Body.Close()

	return nil
}

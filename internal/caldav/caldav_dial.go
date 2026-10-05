package caldav

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"time"
)

// ErrBlockedDestination is wrapped by dial errors when a host resolves
// to an address the SSRF policy refuses (loopback, unspecified,
// link-local including cloud metadata). Callers use errors.Is to give
// such failures a generic user-facing message. (#200)
var ErrBlockedDestination = errors.New("blocked destination")

// caldavDialContext is the dial function used by the CalDAV clients
// built in NewClient and NewOAuthClient. It applies the same policy as
// the ICS client (see icsLoopbackOnlyDialContext): loopback,
// unspecified and link-local addresses are refused at dial time, while
// RFC 1918, CGNAT and unique-local addresses stay allowed because LAN
// CalDAV servers (SOGo, Nextcloud, Radicale) are a real use case.
//
// Package-level variable so httptest-based tests (which bind to
// 127.0.0.1) can swap in a permissive dialer. (#200)
var caldavDialContext = icsLoopbackOnlyDialContext

// SetDialContextForTesting replaces the CalDAV dial function and
// returns a func that restores the previous one. It exists only so
// tests in other packages (e.g. internal/web handler tests against an
// httptest server on 127.0.0.1) can bypass the SSRF guard; production
// code must never call it. Not safe for use by parallel tests. (#200)
func SetDialContextForTesting(fn func(ctx context.Context, network, addr string) (net.Conn, error)) (restore func()) {
	orig := caldavDialContext
	caldavDialContext = fn
	return func() { caldavDialContext = orig }
}

// dialCalDAV indirects through caldavDialContext at dial time, so a
// test override applies even to clients built before the swap.
func dialCalDAV(ctx context.Context, network, addr string) (net.Conn, error) {
	return caldavDialContext(ctx, network, addr)
}

// vettedDialTimeout is the overall connect budget for one guarded dial,
// shared across all of the host's vetted addresses.
const vettedDialTimeout = 30 * time.Second

// dialVettedIPs connects to the first reachable address in ips, which
// the caller has already checked against the SSRF policy. It tries the
// addresses in resolver order and splits the remaining time between
// them the way net.Dialer does for a hostname (each attempt gets an
// equal share, at least 2s), so a dead first A record or an AAAA
// record on a host with no IPv6 route falls back to the next address
// instead of failing the dial. Dialing literal IPs (rather than the
// hostname) keeps the TOCTOU window closed. If every address fails,
// the first error is returned, as net.Dialer does. (#200 review)
func dialVettedIPs(ctx context.Context, network string, ips []net.IP, port string) (net.Conn, error) {
	const minPerAttempt = 2 * time.Second
	deadline := time.Now().Add(vettedDialTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	var firstErr error
	for i, ip := range ips {
		remaining := time.Until(deadline)
		if remaining <= 0 || ctx.Err() != nil {
			break
		}
		perAttempt := remaining / time.Duration(len(ips)-i)
		if perAttempt < minPerAttempt {
			perAttempt = min(minPerAttempt, remaining)
		}
		dialer := &net.Dialer{Timeout: perAttempt, KeepAlive: 30 * time.Second}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("dial %s: %w", net.JoinHostPort(ips[0].String(), port), context.DeadlineExceeded)
	}
	return nil, firstErr
}

// cgnatRange is RFC 6598 shared address space, which net.IP.IsPrivate
// does not cover.
var cgnatRange = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// isNonPublicIP reports whether ip is not publicly routable: RFC 1918,
// unique-local IPv6, CGNAT, loopback, unspecified or link-local.
func isNonPublicIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || cgnatRange.Contains(ip)
}

// IsInternalDialFailure reports whether err is a dial refused by the
// SSRF guard, or a dial (refused, timed out, unreachable) whose target
// address is not publicly routable. User-facing error text for these
// must not distinguish the failure modes, or it becomes an oracle for
// mapping hosts on the server's network. (#200)
func IsInternalDialFailure(err error) bool {
	if errors.Is(err, ErrBlockedDestination) {
		return true
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "dial" || opErr.Addr == nil {
		return false
	}
	host, _, splitErr := net.SplitHostPort(opErr.Addr.String())
	if splitErr != nil {
		host = opErr.Addr.String()
	}
	ip := net.ParseIP(host)
	return ip != nil && isNonPublicIP(ip)
}

// internalDialErrorRe matches the text of a *net.OpError dial failure,
// e.g. "dial tcp 10.0.0.5:22: connect: connection refused", up to the
// end of the line (a dial OpError is always the innermost error, so
// its cause runs to the end of the wrapped message).
var internalDialErrorRe = regexp.MustCompile(`dial (?:tcp|udp)[46]? (\S+): [^\n]*`)

// internalDialScrubbed replaces the cause of a scrubbed dial failure.
const internalDialScrubbed = "could not connect to the server"

// scrubInternalDialErrors rewrites dial failures to non-public
// addresses in msgs so refused, timed-out and unreachable dials read
// identically. Sync errors are stored as text (err.Error()) at many
// call sites and later returned to the user by GET /sources/:id/logs,
// so this works on the text at the single choke point (finishSync)
// rather than on error values. Without it a user could point a source
// at a LAN host:port and read the outcome from the sync log, the same
// oracle categorizeConnectionError closes for the connection test.
// Dial failures to public addresses keep their detail. (#200 review)
func scrubInternalDialErrors(msgs []string) []string {
	for i, msg := range msgs {
		msgs[i] = internalDialErrorRe.ReplaceAllStringFunc(msg, func(m string) string {
			addr := internalDialErrorRe.FindStringSubmatch(m)[1]
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return m
			}
			ip := net.ParseIP(host)
			if ip == nil || !isNonPublicIP(ip) {
				return m
			}
			return "dial tcp " + addr + ": " + internalDialScrubbed
		})
	}
	return msgs
}

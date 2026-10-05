package caldav

import (
	"context"
	"errors"
	"net"
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

// dialCalDAV indirects through caldavDialContext at dial time, so a
// test override applies even to clients built before the swap.
func dialCalDAV(ctx context.Context, network, addr string) (net.Conn, error) {
	return caldavDialContext(ctx, network, addr)
}

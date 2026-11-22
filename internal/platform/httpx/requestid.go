package httpx

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// HeaderRequestID carries the request ID in responses.
const HeaderRequestID = "X-Request-Id"

const (
	channelAPI        = "api"
	maxUserAgent      = 512
	maxInboundRequest = 128
)

// RequestIDConfig configures RequestID.
type RequestIDConfig struct {
	// TrustedProxies are the networks of our own reverse proxies and load
	// balancers. X-Forwarded-For and X-Forwarded-Proto are honored only when
	// the direct peer is in one of them. Empty means trust no proxy.
	TrustedProxies []netip.Prefix
}

// RequestID gives every request a new ids.RequestID, sets the X-Request-Id
// response header, and stores scope.Request{RequestID, IP, UserAgent,
// Channel: "api"} in the context.
//
// An inbound X-Request-Id is never adopted as ours: if it matches a safe
// pattern (1-128 characters of letters, digits and "._:-"), it is recorded
// as inbound_request_id in the access log and span; otherwise it is ignored.
//
// The client IP is the peer address. Only when the peer is a trusted proxy is
// X-Forwarded-For read, from the right: the right-most address that is not a
// trusted proxy is the client, so addresses a client prepends are ignored.
func RequestID(cfg RequestIDConfig) Middleware {
	proxies := make([]netip.Prefix, 0, len(cfg.TrustedProxies))
	for _, p := range cfg.TrustedProxies {
		proxies = append(proxies, unmapPrefix(p))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := ids.New[ids.RequestKind]()
			if err != nil {
				WriteProblem(w, r, err)
				return
			}
			r, st := withState(r)
			peer := peerAddr(r.RemoteAddr)
			trusted := peer.IsValid() && inPrefixes(proxies, peer)
			client := peer
			if trusted {
				client = forwardedClient(peer, r.Header.Values("X-Forwarded-For"), proxies)
			}
			req := scope.Request{
				RequestID: id.String(),
				Channel:   channelAPI,
				UserAgent: truncate(r.UserAgent(), maxUserAgent),
			}
			if client.IsValid() {
				req.IP = client.String()
			}
			inbound := r.Header.Get(HeaderRequestID)
			if !safeRequestID(inbound) {
				inbound = ""
			}
			st.setRequest(req, forwardedScheme(r, trusted), inbound)
			w.Header().Set(HeaderRequestID, req.RequestID)
			next.ServeHTTP(w, r.WithContext(scope.WithRequest(r.Context(), req))) //nolint:contextcheck // derived from the request context
		})
	}
}

// ParsePrefixes parses CIDR prefixes or single IP addresses, for loading
// TrustedProxies from configuration.
func ParsePrefixes(values []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if p, err := netip.ParsePrefix(v); err == nil {
			out = append(out, unmapPrefix(p))
			continue
		}
		a, err := netip.ParseAddr(v)
		if err != nil {
			return nil, errs.Wrap(errs.CodeInvalidRequest, err, fmt.Sprintf("invalid network %q", v))
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// unmapPrefix masks p and turns an IPv4-mapped IPv6 prefix
// (::ffff:a.b.c.d/n with n >= 96) into the IPv4 prefix a.b.c.d/(n-96).
// Peer and forwarded addresses are unmapped before matching, so a mapped
// prefix would otherwise never match and the proxy would not be trusted.
// A prefix shorter than /96 that starts in the mapped range is left as is.
func unmapPrefix(p netip.Prefix) netip.Prefix {
	a := p.Addr()
	if a.Is4In6() && p.Bits() >= 96 {
		return netip.PrefixFrom(a.Unmap(), p.Bits()-96).Masked()
	}
	return p.Masked()
}

func safeRequestID(s string) bool {
	if s == "" || len(s) > maxInboundRequest {
		return false
	}
	for i := range len(s) {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == ':' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

func peerAddr(remote string) netip.Addr {
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return ap.Addr().Unmap().WithZone("")
	}
	if a, err := netip.ParseAddr(remote); err == nil {
		return a.Unmap().WithZone("")
	}
	return netip.Addr{}
}

func parseForwarded(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().WithZone(""), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	return netip.Addr{}, false
}

func inPrefixes(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// forwardedClient walks X-Forwarded-For from the right, starting at the
// trusted peer. The first address that is not a trusted proxy is the client.
// An unparsable entry stops the walk: everything left of it is unverified.
func forwardedClient(peer netip.Addr, headers []string, proxies []netip.Prefix) netip.Addr {
	client := peer
	for i := len(headers) - 1; i >= 0; i-- {
		parts := strings.Split(headers[i], ",")
		for j := len(parts) - 1; j >= 0; j-- {
			a, ok := parseForwarded(parts[j])
			if !ok {
				return client
			}
			client = a
			if !inPrefixes(proxies, a) {
				return client
			}
		}
	}
	return client
}

// forwardedScheme is "https" for TLS connections, or when a trusted proxy
// says so in X-Forwarded-Proto (its right-most value, set by the nearest
// proxy).
func forwardedScheme(r *http.Request, trusted bool) string {
	if r.TLS != nil {
		return "https"
	}
	if trusted {
		values := r.Header.Values("X-Forwarded-Proto")
		if len(values) > 0 {
			parts := strings.Split(values[len(values)-1], ",")
			if strings.EqualFold(strings.TrimSpace(parts[len(parts)-1]), "https") {
				return "https"
			}
		}
	}
	return "http"
}

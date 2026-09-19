package product

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// 只相信直接连接来自显式 CIDR 白名单的代理；从右向左剥离可信代理，防止伪造最左端地址。
func withTrustedProxies(next http.Handler, values []string) http.Handler {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		if p, err := netip.ParsePrefix(value); err == nil {
			prefixes = append(prefixes, p)
		}
	}
	trusted := func(address netip.Addr) bool {
		for _, p := range prefixes {
			if p.Contains(address.Unmap()) {
				return true
			}
		}
		return false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		peer, err := netip.ParseAddr(host)
		if err == nil && trusted(peer) {
			chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
			if len(chain) <= 16 {
				for i := len(chain) - 1; i >= 0 && trusted(peer); i-- {
					value, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
					if err != nil {
						break
					}
					peer = value
				}
			}
			clone := r.Clone(r.Context())
			clone.RemoteAddr = net.JoinHostPort(peer.String(), "0")
			r = clone
		}
		next.ServeHTTP(w, r)
	})
}

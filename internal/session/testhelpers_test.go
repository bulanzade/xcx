package session

import (
	"net"
	"strconv"
)

// splitHostPort is a small test helper returning ip and port (as int).
func splitHostPort(addr string) (ip string, port int) {
	h, ps, err := net.SplitHostPort(addr)
	if err != nil {
		panic(err)
	}
	p, err := strconv.Atoi(ps)
	if err != nil {
		panic(err)
	}
	return h, p
}

// strconvI wraps strconv.Itoa to keep call sites tidy.
func strconvI(i int) string { return strconv.Itoa(i) }

package utils

import (
	"net"
	"strconv"
)

// ParseHostPort safely parses addr like ":4000", "127.0.0.1:4000", "[::1]:4000".
func ParseHostPort(addr string) (string, int) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		// fallback for ":4000"
		if len(addr) > 0 && addr[0] == ':' {
			host = ""
			portStr = addr[1:]
		} else {
			return "", 0
		}
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return host, 0
	}
	return host, p
}

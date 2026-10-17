package utils

import "net"

// SelectEndpointAddress returns a deterministic, IPv4-first pick from addresses: the
// first IPv4 address if present, otherwise the first IPv6 address, otherwise "".
func SelectEndpointAddress(addresses []string) string {
	ipv4, ipv6 := SelectEndpointAddresses(addresses)
	if ipv4 != "" {
		return ipv4
	}
	return ipv6
}

// SelectEndpointAddresses splits addresses by IP family, returning the first IPv4 and
// first IPv6 address found. Entries that aren't valid IPs are ignored.
func SelectEndpointAddresses(addresses []string) (ipv4 string, ipv6 string) {
	for _, address := range addresses {
		ip := net.ParseIP(address)
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			if ipv4 == "" {
				ipv4 = address
			}
		} else if ipv6 == "" {
			ipv6 = address
		}
	}
	return ipv4, ipv6
}

// FormatAddressForURI brackets IPv6 literals (e.g. "::1" -> "[::1]") so they can be
// embedded in a URI-style string. IPv4 addresses and hostnames are returned unchanged.
func FormatAddressForURI(address string) string {
	ip := net.ParseIP(address)
	if ip != nil && ip.To4() == nil {
		return "[" + address + "]"
	}
	return address
}

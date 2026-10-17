package utils

import "testing"

func TestSelectEndpointAddresses(t *testing.T) {
	tests := []struct {
		name          string
		addresses     []string
		wantIPv4      string
		wantIPv6      string
		wantSelection string
	}{
		{
			name:          "ipv4 only",
			addresses:     []string{"10.0.0.1"},
			wantIPv4:      "10.0.0.1",
			wantIPv6:      "",
			wantSelection: "10.0.0.1",
		},
		{
			name:          "ipv6 only",
			addresses:     []string{"fd00::1"},
			wantIPv4:      "",
			wantIPv6:      "fd00::1",
			wantSelection: "fd00::1",
		},
		{
			name:          "dual-stack ipv4 first",
			addresses:     []string{"10.0.0.1", "fd00::1"},
			wantIPv4:      "10.0.0.1",
			wantIPv6:      "fd00::1",
			wantSelection: "10.0.0.1",
		},
		{
			name:          "dual-stack ipv6 first",
			addresses:     []string{"fd00::1", "10.0.0.1"},
			wantIPv4:      "10.0.0.1",
			wantIPv6:      "fd00::1",
			wantSelection: "10.0.0.1",
		},
		{
			name:          "empty",
			addresses:     nil,
			wantIPv4:      "",
			wantIPv6:      "",
			wantSelection: "",
		},
		{
			name:          "invalid entries ignored",
			addresses:     []string{"not-an-ip", "fd00::1"},
			wantIPv4:      "",
			wantIPv6:      "fd00::1",
			wantSelection: "fd00::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipv4, ipv6 := SelectEndpointAddresses(tt.addresses)
			if ipv4 != tt.wantIPv4 {
				t.Errorf("ipv4 = %q, want %q", ipv4, tt.wantIPv4)
			}
			if ipv6 != tt.wantIPv6 {
				t.Errorf("ipv6 = %q, want %q", ipv6, tt.wantIPv6)
			}
			if got := SelectEndpointAddress(tt.addresses); got != tt.wantSelection {
				t.Errorf("SelectEndpointAddress() = %q, want %q", got, tt.wantSelection)
			}
		})
	}
}

func TestFormatAddressForURI(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    string
	}{
		{"ipv4", "10.0.0.1", "10.0.0.1"},
		{"ipv6", "fd00::1", "[fd00::1]"},
		{"hostname", "example.com", "example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatAddressForURI(tt.address); got != tt.want {
				t.Errorf("FormatAddressForURI(%q) = %q, want %q", tt.address, got, tt.want)
			}
		})
	}
}

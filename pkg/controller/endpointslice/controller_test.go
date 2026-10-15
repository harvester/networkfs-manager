package endpointslice

import (
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"
)

func boolPtr(b bool) *bool { return &b }

func sliceWithAddresses(addressType discoveryv1.AddressType, ready *bool, addresses ...string) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		AddressType: addressType,
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses: addresses,
				Conditions: discoveryv1.EndpointConditions{
					Ready: ready,
				},
			},
		},
	}
}

func TestFirstReadyAddresses(t *testing.T) {
	tests := []struct {
		name     string
		slices   []*discoveryv1.EndpointSlice
		wantIPv4 string
		wantIPv6 string
	}{
		{
			name: "single IPv4 slice",
			slices: []*discoveryv1.EndpointSlice{
				sliceWithAddresses(discoveryv1.AddressTypeIPv4, nil, "10.0.0.1"),
			},
			wantIPv4: "10.0.0.1",
			wantIPv6: "",
		},
		{
			name: "single IPv6 slice",
			slices: []*discoveryv1.EndpointSlice{
				sliceWithAddresses(discoveryv1.AddressTypeIPv6, nil, "fd00::1"),
			},
			wantIPv4: "",
			wantIPv6: "fd00::1",
		},
		{
			name: "dual-stack: one IPv4 slice + one IPv6 slice, merged",
			slices: []*discoveryv1.EndpointSlice{
				sliceWithAddresses(discoveryv1.AddressTypeIPv4, nil, "10.0.0.1"),
				sliceWithAddresses(discoveryv1.AddressTypeIPv6, nil, "fd00::1"),
			},
			wantIPv4: "10.0.0.1",
			wantIPv6: "fd00::1",
		},
		{
			name: "dual-stack: IPv6 slice listed before IPv4 slice, still merged",
			slices: []*discoveryv1.EndpointSlice{
				sliceWithAddresses(discoveryv1.AddressTypeIPv6, nil, "fd00::1"),
				sliceWithAddresses(discoveryv1.AddressTypeIPv4, nil, "10.0.0.1"),
			},
			wantIPv4: "10.0.0.1",
			wantIPv6: "fd00::1",
		},
		{
			name: "IPv4 slice not ready yet, only IPv6 available (fallback case)",
			slices: []*discoveryv1.EndpointSlice{
				sliceWithAddresses(discoveryv1.AddressTypeIPv4, boolPtr(false), "10.0.0.1"),
				sliceWithAddresses(discoveryv1.AddressTypeIPv6, nil, "fd00::1"),
			},
			wantIPv4: "",
			wantIPv6: "fd00::1",
		},
		{
			name: "nil Ready condition is treated as ready",
			slices: []*discoveryv1.EndpointSlice{
				sliceWithAddresses(discoveryv1.AddressTypeIPv4, nil, "10.0.0.1"),
			},
			wantIPv4: "10.0.0.1",
			wantIPv6: "",
		},
		{
			name: "explicit Ready=true is treated as ready",
			slices: []*discoveryv1.EndpointSlice{
				sliceWithAddresses(discoveryv1.AddressTypeIPv4, boolPtr(true), "10.0.0.1"),
			},
			wantIPv4: "10.0.0.1",
			wantIPv6: "",
		},
		{
			name: "both slices not ready",
			slices: []*discoveryv1.EndpointSlice{
				sliceWithAddresses(discoveryv1.AddressTypeIPv4, boolPtr(false), "10.0.0.1"),
				sliceWithAddresses(discoveryv1.AddressTypeIPv6, boolPtr(false), "fd00::1"),
			},
			wantIPv4: "",
			wantIPv6: "",
		},
		{
			name:     "no slices",
			slices:   nil,
			wantIPv4: "",
			wantIPv6: "",
		},
		{
			name: "slice with no endpoints",
			slices: []*discoveryv1.EndpointSlice{
				{AddressType: discoveryv1.AddressTypeIPv4},
			},
			wantIPv4: "",
			wantIPv6: "",
		},
		{
			name: "endpoint with no addresses",
			slices: []*discoveryv1.EndpointSlice{
				sliceWithAddresses(discoveryv1.AddressTypeIPv4, nil),
			},
			wantIPv4: "",
			wantIPv6: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipv4, ipv6 := firstReadyAddresses(tt.slices)
			if ipv4 != tt.wantIPv4 {
				t.Errorf("ipv4 = %q, want %q", ipv4, tt.wantIPv4)
			}
			if ipv6 != tt.wantIPv6 {
				t.Errorf("ipv6 = %q, want %q", ipv6, tt.wantIPv6)
			}
		})
	}
}

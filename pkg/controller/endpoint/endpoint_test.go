package endpoint

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type fakeServiceClient struct {
	getService   *corev1.Service
	listServices *corev1.ServiceList
	getErr       error
	listErr      error
	getCalls     int
	listCalls    int
	listSelector string
}

func (f *fakeServiceClient) Get(_, _ string, _ metav1.GetOptions) (*corev1.Service, error) {
	f.getCalls++
	return f.getService, f.getErr
}

func (f *fakeServiceClient) List(_ string, options metav1.ListOptions) (*corev1.ServiceList, error) {
	f.listCalls++
	f.listSelector = options.LabelSelector
	if f.listServices == nil {
		f.listServices = &corev1.ServiceList{}
	}
	return f.listServices, f.listErr
}

func TestVolumeName(t *testing.T) {
	tests := []struct {
		name       string
		service    *corev1.Service
		wantName   string
		wantMapped bool
	}{
		{
			name: "RWX label takes priority over service name",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "share-manager-service",
					Labels: map[string]string{RWXVolumeServiceLabel: "pvc-labeled"},
				},
			},
			wantName:   "pvc-labeled",
			wantMapped: true,
		},
		{
			name:       "legacy pvc service",
			service:    &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "pvc-legacy"}},
			wantName:   "pvc-legacy",
			wantMapped: true,
		},
		{
			name:       "unrelated service",
			service:    &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes"}},
			wantMapped: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotMapped := VolumeName(tt.service)
			if gotName != tt.wantName || gotMapped != tt.wantMapped {
				t.Fatalf("VolumeName() = (%q, %v), want (%q, %v)", gotName, gotMapped, tt.wantName, tt.wantMapped)
			}
		})
	}
}

func TestSelect(t *testing.T) {
	const volumeName = "pvc-test"

	tests := []struct {
		name    string
		service *corev1.Service
		want    Selection
	}{
		{
			name: "labeled load balancer IP has top priority",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{RWXVolumeServiceLabel: volumeName},
				},
				Spec: corev1.ServiceSpec{
					ClusterIP:      corev1.ClusterIPNone,
					LoadBalancerIP: "182.16.0.6",
				},
			},
			want: Selection{Address: "182.16.0.6", Source: SourceLoadBalancerIP},
		},
		{
			name: "matching label remains authoritative while IP is pending",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{RWXVolumeServiceLabel: volumeName},
				},
				Spec: corev1.ServiceSpec{ClusterIP: "10.53.0.8"},
			},
			want: Selection{Source: SourceLoadBalancerIP},
		},
		{
			name: "different label value keeps legacy ClusterIP logic",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{RWXVolumeServiceLabel: "pvc-other"},
				},
				Spec: corev1.ServiceSpec{
					ClusterIP:      "10.53.0.8",
					LoadBalancerIP: "182.16.0.6",
				},
			},
			want: Selection{Address: "10.53.0.8", Source: SourceClusterIP},
		},
		{
			name: "regular service uses ClusterIP",
			service: &corev1.Service{
				Spec: corev1.ServiceSpec{ClusterIP: "10.53.0.8"},
			},
			want: Selection{Address: "10.53.0.8", Source: SourceClusterIP},
		},
		{
			name: "headless service uses EndpointSlice",
			service: &corev1.Service{
				Spec: corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone},
			},
			want: Selection{Source: SourceEndpointSlice},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Select(tt.service, volumeName); got != tt.want {
				t.Fatalf("Select() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSelectionUsesServiceAddress(t *testing.T) {
	tests := []struct {
		source Source
		want   bool
	}{
		{source: SourceLoadBalancerIP, want: true},
		{source: SourceClusterIP, want: true},
		{source: SourceEndpointSlice, want: false},
		{source: Source("unknown"), want: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.source), func(t *testing.T) {
			selection := Selection{Source: tt.source}
			if got := selection.UsesServiceAddress(); got != tt.want {
				t.Fatalf("UsesServiceAddress() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveService(t *testing.T) {
	const volumeName = "pvc-test"

	t.Run("unmanaged service is ignored", func(t *testing.T) {
		client := &fakeServiceClient{}
		service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes"}}

		gotVolume, gotSelection, managed, err := ResolveService(client, "longhorn-system", service)
		if err != nil {
			t.Fatal(err)
		}
		if managed || gotVolume != "" || gotSelection != (Selection{}) {
			t.Fatalf("ResolveService() = (%q, %#v, %v), want unmanaged", gotVolume, gotSelection, managed)
		}
		if client.listCalls != 0 {
			t.Fatalf("ResolveService() made %d list calls, want 0", client.listCalls)
		}
	})

	t.Run("returns volume and selected endpoint", func(t *testing.T) {
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:   volumeName,
				Labels: map[string]string{RWXVolumeServiceLabel: volumeName},
			},
			Spec: corev1.ServiceSpec{
				ClusterIP:      corev1.ClusterIPNone,
				LoadBalancerIP: "182.16.0.6",
			},
		}
		client := &fakeServiceClient{
			listServices: &corev1.ServiceList{Items: []corev1.Service{*service}},
		}

		gotVolume, gotSelection, managed, err := ResolveService(
			client,
			"longhorn-system",
			service,
		)
		if err != nil {
			t.Fatal(err)
		}
		wantSelection := Selection{Address: "182.16.0.6", Source: SourceLoadBalancerIP}
		if !managed || gotVolume != volumeName || gotSelection != wantSelection {
			t.Fatalf("ResolveService() = (%q, %#v, %v), want (%q, %#v, true)",
				gotVolume, gotSelection, managed, volumeName, wantSelection)
		}
	})

	t.Run("preserves managed volume context on error", func(t *testing.T) {
		wantErr := errors.New("list failed")
		service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: volumeName}}

		gotVolume, _, managed, err := ResolveService(
			&fakeServiceClient{listErr: wantErr},
			"longhorn-system",
			service,
		)
		if !errors.Is(err, wantErr) || !managed || gotVolume != volumeName {
			t.Fatalf("ResolveService() = (%q, _, %v, %v), want (%q, _, true, %v)",
				gotVolume, managed, err, volumeName, wantErr)
		}
	})
}

func TestFindService(t *testing.T) {
	const volumeName = "pvc-test"

	t.Run("labeled service takes priority over conventional service", func(t *testing.T) {
		labeled := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "rwx-service",
				Labels: map[string]string{RWXVolumeServiceLabel: volumeName},
			},
		}
		client := &fakeServiceClient{
			listServices: &corev1.ServiceList{Items: []corev1.Service{labeled}},
		}

		got, err := FindService(client, "longhorn-system", volumeName)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != labeled.Name {
			t.Fatalf("FindService() returned %q, want %q", got.Name, labeled.Name)
		}
		wantSelector := RWXVolumeServiceLabel + "=" + volumeName
		if client.listSelector != wantSelector {
			t.Fatalf("FindService() selector = %q, want %q", client.listSelector, wantSelector)
		}
		if client.getCalls != 0 {
			t.Fatalf("FindService() made %d get calls, want 0", client.getCalls)
		}
	})

	t.Run("same-name service is fetched when no label matches", func(t *testing.T) {
		conventional := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: volumeName}}
		client := &fakeServiceClient{getService: conventional}

		got, err := FindService(client, "longhorn-system", volumeName)
		if err != nil {
			t.Fatal(err)
		}
		if got != conventional || client.getCalls != 1 {
			t.Fatalf("FindService() returned %#v after %d get calls", got, client.getCalls)
		}
	})

	t.Run("multiple labeled services are rejected", func(t *testing.T) {
		client := &fakeServiceClient{
			listServices: &corev1.ServiceList{Items: []corev1.Service{{}, {}}},
		}

		if _, err := FindService(client, "longhorn-system", volumeName); err == nil {
			t.Fatal("FindService() error = nil, want ambiguity error")
		}
	})

	t.Run("list errors are returned", func(t *testing.T) {
		wantErr := errors.New("list failed")
		client := &fakeServiceClient{listErr: wantErr}

		if _, err := FindService(client, "longhorn-system", volumeName); !errors.Is(err, wantErr) {
			t.Fatalf("FindService() error = %v, want %v", err, wantErr)
		}
	})

	t.Run("get errors are returned", func(t *testing.T) {
		wantErr := errors.New("get failed")
		client := &fakeServiceClient{getErr: wantErr}

		if _, err := FindService(client, "longhorn-system", volumeName); !errors.Is(err, wantErr) {
			t.Fatalf("FindService() error = %v, want %v", err, wantErr)
		}
	})
}

func TestSelectFromSlices(t *testing.T) {
	nfsPort := "nfs"
	httpPort := "http"

	tests := []struct {
		name      string
		slices    []discoveryv1.EndpointSlice
		want      string
		wantReady bool
		wantError bool
	}{
		{
			name: "no slices is not ready",
		},
		{
			name: "slice without endpoints is not ready",
			slices: []discoveryv1.EndpointSlice{{
				Ports: []discoveryv1.EndpointPort{{Name: &nfsPort}},
			}},
		},
		{
			name: "endpoint without addresses is not ready",
			slices: []discoveryv1.EndpointSlice{{
				Endpoints: []discoveryv1.Endpoint{{}},
				Ports:     []discoveryv1.EndpointPort{{Name: &nfsPort}},
			}},
		},
		{
			name: "single NFS address is ready",
			slices: []discoveryv1.EndpointSlice{{
				Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.8"}}},
				Ports:     []discoveryv1.EndpointPort{{Name: &nfsPort}},
			}},
			want:      "10.0.0.8",
			wantReady: true,
		},
		{
			name: "multiple slices are rejected",
			slices: []discoveryv1.EndpointSlice{
				{},
				{},
			},
			wantError: true,
		},
		{
			name: "multiple endpoints are rejected",
			slices: []discoveryv1.EndpointSlice{{
				Endpoints: []discoveryv1.Endpoint{
					{Addresses: []string{"10.0.0.8"}},
					{Addresses: []string{"10.0.0.9"}},
				},
				Ports: []discoveryv1.EndpointPort{{Name: &nfsPort}},
			}},
			wantError: true,
		},
		{
			name: "multiple addresses are rejected",
			slices: []discoveryv1.EndpointSlice{{
				Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.8", "10.0.0.9"}}},
				Ports:     []discoveryv1.EndpointPort{{Name: &nfsPort}},
			}},
			wantError: true,
		},
		{
			name: "missing NFS port is rejected",
			slices: []discoveryv1.EndpointSlice{{
				Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.8"}}},
			}},
			wantError: true,
		},
		{
			name: "non-NFS port is rejected",
			slices: []discoveryv1.EndpointSlice{{
				Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.8"}}},
				Ports:     []discoveryv1.EndpointPort{{Name: &httpPort}},
			}},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ready, err := SelectFromSlices("pvc-test", tt.slices)
			if (err != nil) != tt.wantError {
				t.Fatalf("SelectFromSlices() error = %v, wantError %v", err, tt.wantError)
			}
			if got != tt.want || ready != tt.wantReady {
				t.Fatalf("SelectFromSlices() = (%q, %v), want (%q, %v)", got, ready, tt.want, tt.wantReady)
			}
		})
	}
}

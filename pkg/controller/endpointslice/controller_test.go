package endpointslice

import (
	"errors"
	"reflect"
	"testing"

	ctlcorev1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/harvester/networkfs-manager/pkg/controller/endpoint"
	ctlnfs "github.com/harvester/networkfs-manager/pkg/generated/controllers/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/utils"
)

type fakeServiceCache struct {
	ctlcorev1.ServiceCache
	service *corev1.Service
	err     error
	keys    []string
}

func (f *fakeServiceCache) Get(namespace, name string) (*corev1.Service, error) {
	f.keys = append(f.keys, namespace+"/"+name)
	return f.service, f.err
}

type fakeNetworkFS struct {
	ctlnfs.NetworkFilesystemController
	keys []string
}

func (f *fakeNetworkFS) Enqueue(namespace, name string) {
	f.keys = append(f.keys, namespace+"/"+name)
}

func TestOnEndpointSliceChange(t *testing.T) {
	cacheErr := errors.New("cache unavailable")
	missing := apierrors.NewNotFound(schema.GroupResource{Resource: "services"}, "pvc-test")
	now := metav1.Now()
	tests := []struct {
		name      string
		change    func(*discoveryv1.EndpointSlice)
		service   *corev1.Service
		cacheErr  error
		wantKey   string
		wantError error
		wantGet   bool
	}{
		{name: "nil event"},
		{name: "deleting slice", change: func(s *discoveryv1.EndpointSlice) { s.DeletionTimestamp = &now }},
		{name: "other namespace", change: func(s *discoveryv1.EndpointSlice) { s.Namespace = "default" }},
		{name: "missing service label", change: func(s *discoveryv1.EndpointSlice) { s.Labels = nil }},
		{name: "missing owner", cacheErr: missing, wantGet: true},
		{name: "cache error", cacheErr: cacheErr, wantError: cacheErr, wantGet: true},
		{
			name: "unmanaged service", wantGet: true,
			service: &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "unrelated"}},
		},
		{
			name: "legacy headless service", wantGet: true, wantKey: "harvester-system/pvc-test",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "pvc-test"},
				Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone},
			},
		},
		{
			name: "labeled service maps to volume", wantGet: true, wantKey: "harvester-system/pvc-labeled",
			service: &corev1.Service{ObjectMeta: metav1.ObjectMeta{
				Name: "rwx-service", Labels: map[string]string{endpoint.RWXVolumeServiceLabel: "pvc-labeled"},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{
				Name: "pvc-test-generated", Namespace: utils.LHNameSpace,
				Labels: map[string]string{discoveryv1.LabelServiceName: "pvc-test"},
			}}
			if tt.name == "nil event" {
				slice = nil
			}
			if tt.change != nil {
				tt.change(slice)
			}
			before := slice.DeepCopy()
			services := &fakeServiceCache{service: tt.service, err: tt.cacheErr}
			nfs := &fakeNetworkFS{}
			c := &Controller{namespace: "harvester-system", serviceCache: services, NetworkFilsystems: nfs}
			_, err := c.OnEndpointSliceChange("", slice)
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("error = %v, want %v", err, tt.wantError)
			}
			var wantKeys, wantGets []string
			if tt.wantKey != "" {
				wantKeys = []string{tt.wantKey}
			}
			if tt.wantGet {
				wantGets = []string{utils.LHNameSpace + "/pvc-test"}
			}
			if !reflect.DeepEqual(nfs.keys, wantKeys) || !reflect.DeepEqual(services.keys, wantGets) {
				t.Fatalf("queued %v, looked up %v; want %v, %v", nfs.keys, services.keys, wantKeys, wantGets)
			}
			if !reflect.DeepEqual(slice, before) {
				t.Fatal("handler modified the cached EndpointSlice")
			}
		})
	}
}

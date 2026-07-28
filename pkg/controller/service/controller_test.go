package service

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/harvester/networkfs-manager/pkg/controller/endpoint"
	ctlnfs "github.com/harvester/networkfs-manager/pkg/generated/controllers/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/utils"
)

func TestShouldHandleService(t *testing.T) {
	tests := []struct {
		name    string
		service *corev1.Service
		want    bool
	}{
		{
			name: "unlabeled Longhorn Service",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Namespace: utils.LHNameSpace},
			},
			want: true,
		},
		{
			name: "labeled Longhorn Service",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: utils.LHNameSpace,
					Labels: map[string]string{
						endpoint.RWXVolumeServiceLabel: "pvc-test",
					},
				},
			},
			want: true,
		},
		{
			name: "labeled Service outside Longhorn namespace",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "harvester-system",
					Labels: map[string]string{
						endpoint.RWXVolumeServiceLabel: "pvc-test",
					},
				},
			},
			want: false,
		},
		{
			name: "unlabeled Service outside Longhorn namespace",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Namespace: "cattle-system"},
			},
			want: false,
		},
		{
			name: "empty RWX label outside Longhorn namespace",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "harvester-system",
					Labels: map[string]string{
						endpoint.RWXVolumeServiceLabel: "",
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldHandleService(tt.service); got != tt.want {
				t.Fatalf("shouldHandleService() = %v, want %v", got, tt.want)
			}
		})
	}
}

type fakeNetworkFS struct {
	ctlnfs.NetworkFilesystemController
	keys []string
}

func (f *fakeNetworkFS) Enqueue(namespace, name string) {
	f.keys = append(f.keys, namespace+"/"+name)
}

func TestOnServicesChange(t *testing.T) {
	now := metav1.Now()
	tests := []struct {
		name    string
		service *corev1.Service
		want    string
	}{
		{name: "nil event"},
		{name: "deleting service", service: &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc-test", Namespace: utils.LHNameSpace, DeletionTimestamp: &now},
		}},
		{name: "other namespace", service: &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc-test", Namespace: "default"},
		}},
		{name: "unmanaged service", service: &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: utils.LHNameSpace},
		}},
		{name: "legacy headless service", service: &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc-test", Namespace: utils.LHNameSpace},
			Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone},
		}, want: "harvester-system/pvc-test"},
		{name: "pending VIP", service: &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name: "rwx-service", Namespace: utils.LHNameSpace,
				Labels: map[string]string{endpoint.RWXVolumeServiceLabel: "pvc-labeled"},
			},
		}, want: "harvester-system/pvc-labeled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nfs := &fakeNetworkFS{}
			c := &Controller{namespace: "harvester-system", NetworkFilsystems: nfs}
			if _, err := c.OnServicesChange("", tt.service); err != nil {
				t.Fatal(err)
			}
			var want []string
			if tt.want != "" {
				want = []string{tt.want}
			}
			if !reflect.DeepEqual(nfs.keys, want) {
				t.Fatalf("queued %v, want %v", nfs.keys, want)
			}
		})
	}
}

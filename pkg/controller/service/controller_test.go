package service

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/harvester/networkfs-manager/pkg/controller/endpoint"
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
			name: "labeled Service outside Longhorn namespace",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "harvester-system",
					Labels: map[string]string{
						endpoint.RWXVolumeServiceLabel: "pvc-test",
					},
				},
			},
			want: true,
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

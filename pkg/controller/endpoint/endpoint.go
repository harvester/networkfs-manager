package endpoint

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	// RWXVolumeServiceLabel identifies a Service that exposes the stable endpoint
	// for a Longhorn RWX volume. The label value is the volume name.
	RWXVolumeServiceLabel = "harvesterhci.io/rwx-vol-service"
)

type Source string

const (
	SourceLoadBalancerIP Source = "loadBalancerIP"
	SourceClusterIP      Source = "clusterIP"
	SourceEndpointSlice  Source = "endpointSlice"
)

type Selection struct {
	Address string
	Source  Source
}

// UsesServiceAddress reports whether the endpoint is published directly on the
// Service rather than through an EndpointSlice.
func (s Selection) UsesServiceAddress() bool {
	return s.Source == SourceLoadBalancerIP || s.Source == SourceClusterIP
}

type ServiceCache interface {
	Get(namespace, name string) (*corev1.Service, error)
	List(namespace string, selector labels.Selector) ([]*corev1.Service, error)
}

// VolumeName returns the NetworkFilesystem name managed by a Service.
// The RWX label is authoritative; the pvc- service name is the legacy fallback.
func VolumeName(service *corev1.Service) (string, bool) {
	if volumeName := service.Labels[RWXVolumeServiceLabel]; volumeName != "" {
		return volumeName, true
	}
	if strings.HasPrefix(service.Name, "pvc-") {
		return service.Name, true
	}
	return "", false
}

// Select chooses the endpoint source for a Service in priority order:
// labeled RWX loadBalancerIP, regular ClusterIP, then EndpointSlice.
//
// A matching RWX label remains authoritative when loadBalancerIP is empty so
// lower-priority, transient addresses cannot overwrite the desired endpoint.
func Select(service *corev1.Service, volumeName string) Selection {
	if service.Labels[RWXVolumeServiceLabel] == volumeName {
		return Selection{
			Address: service.Spec.LoadBalancerIP,
			Source:  SourceLoadBalancerIP,
		}
	}
	if service.Spec.ClusterIP != corev1.ClusterIPNone {
		return Selection{
			Address: service.Spec.ClusterIP,
			Source:  SourceClusterIP,
		}
	}
	return Selection{Source: SourceEndpointSlice}
}

// FindService returns the labeled RWX Service when one exists. The conventional
// same-name Service is used when no labeled Service matches the volume.
func FindService(cache ServiceCache, namespace, volumeName string) (*corev1.Service, error) {
	services, err := cache.List(namespace, labels.SelectorFromSet(labels.Set{
		RWXVolumeServiceLabel: volumeName,
	}))
	if err != nil {
		return nil, err
	}
	switch len(services) {
	case 0:
		return cache.Get(namespace, volumeName)
	case 1:
		return services[0], nil
	default:
		return nil, fmt.Errorf("network filesystem %s has more than one service labeled %s=%s",
			volumeName, RWXVolumeServiceLabel, volumeName)
	}
}

// SelectFromSlices validates the EndpointSlices for an NFS Service and returns
// its single endpoint address. No slices or no address means the Service is not
// ready yet.
func SelectFromSlices(serviceName string, endpointSlices []*discoveryv1.EndpointSlice) (string, bool, error) {
	if len(endpointSlices) == 0 {
		return "", false, nil
	}
	if len(endpointSlices) > 1 {
		return "", false, fmt.Errorf("service %s has more than one endpointslice", serviceName)
	}

	endpointSlice := endpointSlices[0]
	if len(endpointSlice.Endpoints) == 0 || len(endpointSlice.Endpoints[0].Addresses) == 0 {
		return "", false, nil
	}
	if len(endpointSlice.Endpoints) > 1 || len(endpointSlice.Endpoints[0].Addresses) > 1 || len(endpointSlice.Ports) > 1 {
		return "", false, fmt.Errorf("endpointslice of service %s has more than one endpoint", serviceName)
	}
	if len(endpointSlice.Ports) == 0 || endpointSlice.Ports[0].Name == nil || *endpointSlice.Ports[0].Name != "nfs" {
		return "", false, fmt.Errorf("endpointslice of service %s has no nfs port", serviceName)
	}
	// A nil Ready condition is treated as ready by the EndpointSlice API.
	if ready := endpointSlice.Endpoints[0].Conditions.Ready; ready != nil && !*ready {
		return "", false, nil
	}
	return endpointSlice.Endpoints[0].Addresses[0], true, nil
}

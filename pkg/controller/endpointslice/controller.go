package endpointslice

import (
	"context"

	ctlcorev1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	ctldiscoveryv1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/discovery/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/harvester/networkfs-manager/pkg/controller/endpoint"
	ctlntefsv1 "github.com/harvester/networkfs-manager/pkg/generated/controllers/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/utils"
)

type Controller struct {
	namespace string

	EndpointSlices    ctldiscoveryv1.EndpointSliceController
	NetworkFilsystems ctlntefsv1.NetworkFilesystemController
	serviceCache      ctlcorev1.ServiceCache
}

const (
	netFSEndpointSliceHandlerName = "harvester-netfs-endpointslice-handler"
)

// Register watches EndpointSlice changes that affect NetworkFilesystem endpoints.
func Register(
	ctx context.Context,
	endpointSlices ctldiscoveryv1.EndpointSliceController,
	netfilesystems ctlntefsv1.NetworkFilesystemController,
	services ctlcorev1.ServiceController,
	opt *utils.Option,
) error {
	c := &Controller{
		namespace:         opt.Namespace,
		EndpointSlices:    endpointSlices,
		NetworkFilsystems: netfilesystems,
		serviceCache:      services.Cache(),
	}

	c.EndpointSlices.OnChange(ctx, netFSEndpointSliceHandlerName, c.OnEndpointSliceChange)
	return nil
}

// OnEndpointSliceChange queues the volume without retaining the event's address.
func (c *Controller) OnEndpointSliceChange(
	_ string,
	endpointSlice *discoveryv1.EndpointSlice,
) (*discoveryv1.EndpointSlice, error) {
	if endpointSlice == nil || endpointSlice.DeletionTimestamp != nil {
		logrus.Infof("Skip this round because endpointslice is deleted or deleting")
		return nil, nil
	}
	if endpointSlice.Namespace != utils.LHNameSpace {
		return nil, nil
	}

	service, err := c.getOwningService(endpointSlice)
	if err != nil || service == nil {
		return nil, err
	}

	volumeName, managed := endpoint.VolumeName(service)
	if !managed {
		return nil, nil
	}
	c.NetworkFilsystems.Enqueue(c.namespace, volumeName)
	return nil, nil
}

// getOwningService returns nil when the service label or the owning Service is missing.
func (c *Controller) getOwningService(endpointSlice *discoveryv1.EndpointSlice) (*corev1.Service, error) {
	// The slice name has a generated suffix; the label identifies the owning Service.
	svcName := endpointSlice.Labels[discoveryv1.LabelServiceName]
	if svcName == "" {
		return nil, nil
	}

	service, err := c.serviceCache.Get(endpointSlice.Namespace, svcName)
	if apierrors.IsNotFound(err) {
		logrus.Infof("Skip stale endpointslice %s because service %s no longer exists", endpointSlice.Name, svcName)
		return nil, nil
	}
	if err != nil {
		logrus.Errorf("Failed to get service %s: %v", svcName, err)
		return nil, err
	}
	return service, nil
}

package endpointslice

import (
	"context"

	ctlcorev1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	ctldiscoveryv1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/discovery/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/harvester/networkfs-manager/pkg/controller/endpoint"
	networkfsstatus "github.com/harvester/networkfs-manager/pkg/controller/networkfilesystem/status"
	ctlntefsv1 "github.com/harvester/networkfs-manager/pkg/generated/controllers/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/utils"
)

type Controller struct {
	namespace string
	nodeName  string

	EndpointSliceCache ctldiscoveryv1.EndpointSliceCache
	EndpointSlices     ctldiscoveryv1.EndpointSliceController
	NetworkFSCache     ctlntefsv1.NetworkFilesystemCache
	NetworkFilsystems  ctlntefsv1.NetworkFilesystemController

	serviceClient ctlcorev1.ServiceController
}

const (
	netFSEndpointSliceHandlerName = "harvester-netfs-endpointslice-handler"
)

// Register register the endpointslice controller
func Register(ctx context.Context, endpointSlices ctldiscoveryv1.EndpointSliceController, netfilesystems ctlntefsv1.NetworkFilesystemController, serviceClient ctlcorev1.ServiceController, opt *utils.Option) error {

	c := &Controller{
		namespace:          opt.Namespace,
		nodeName:           opt.NodeName,
		EndpointSlices:     endpointSlices,
		EndpointSliceCache: endpointSlices.Cache(),
		NetworkFilsystems:  netfilesystems,
		NetworkFSCache:     netfilesystems.Cache(),
		serviceClient:      serviceClient,
	}

	c.EndpointSlices.OnChange(ctx, netFSEndpointSliceHandlerName, c.OnEndpointSliceChange)
	return nil
}

// OnEndpointSliceChange watch the endpointslice on change and sync up to the networkfilesystem CR
func (c *Controller) OnEndpointSliceChange(_ string, endpointSlice *discoveryv1.EndpointSlice) (*discoveryv1.EndpointSlice, error) {
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

	volumeName, selection, managed, err := endpoint.ResolveService(c.serviceClient, service.Namespace, service)
	if err != nil {
		logrus.Errorf("Failed to resolve endpoint service for networkFS %s: %v", volumeName, err)
		return nil, err
	}
	if !managed {
		return nil, nil
	}
	if selection.Source != endpoint.SourceEndpointSlice {
		logrus.Infof("Skip endpointslice update because networkFS %s uses %s", volumeName, selection.Source)
		return nil, nil
	}

	logrus.Infof("Handling endpointslice %s (service %s) change event for network filesystem %s",
		endpointSlice.Name, service.Name, volumeName)

	selection.Address = firstReadyAddress(endpointSlice)
	if err := networkfsstatus.ReconcileEndpoint(c.NetworkFilsystems, c.namespace, volumeName, selection); err != nil {
		logrus.Errorf("Failed to reconcile endpoint for networkFS %s: %v", volumeName, err)
		return nil, err
	}
	return nil, nil
}

// getOwningService returns nil when the service label or the owning Service is missing.
func (c *Controller) getOwningService(endpointSlice *discoveryv1.EndpointSlice) (*corev1.Service, error) {
	// The slice name has a generated suffix; the label identifies the owning Service.
	svcName := endpointSlice.Labels[discoveryv1.LabelServiceName]
	if svcName == "" {
		return nil, nil
	}

	service, err := c.serviceClient.Get(endpointSlice.Namespace, svcName, metav1.GetOptions{})
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

// firstReadyAddress returns the first address of a ready endpoint in the slice.
// A nil Ready condition is treated as ready, as defined by the EndpointSlice API.
func firstReadyAddress(endpointSlice *discoveryv1.EndpointSlice) string {
	for _, endpoint := range endpointSlice.Endpoints {
		if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
			continue
		}
		if len(endpoint.Addresses) > 0 {
			return endpoint.Addresses[0]
		}
	}
	return ""
}

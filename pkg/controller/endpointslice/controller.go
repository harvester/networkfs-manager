package endpointslice

import (
	"context"
	"reflect"
	"strings"

	ctlcorev1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	ctldiscoveryv1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/discovery/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	networkfsv1 "github.com/harvester/networkfs-manager/pkg/apis/harvesterhci.io/v1beta1"
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

	// the endpointslice name has a generated suffix, the owning service name is kept in the well-known label
	svcName := endpointSlice.Labels[discoveryv1.LabelServiceName]

	// we only care about the endpointslice of the service with name prefix "pvc-"
	if !strings.HasPrefix(svcName, "pvc-") {
		return nil, nil
	}

	logrus.Infof("Handling endpointslice %s (service %s) change event", endpointSlice.Name, svcName)
	networkFS, err := c.NetworkFilsystems.Get(c.namespace, svcName, metav1.GetOptions{})
	if err != nil {
		logrus.Errorf("Failed to get networkFS %s: %v", svcName, err)
		return nil, err
	}

	// only update when the networkfilesystem is enabled.
	if networkFS.Spec.DesiredState != networkfsv1.NetworkFSStateEnabled {
		logrus.Infof("Skip update with endpointslice change event because networkfilesystem %s is not enabled", networkFS.Name)
		return nil, nil
	}

	// skip update if the service.Spec.ClusterIP is not ClusterIPNone (means the we depends on service)
	service, err := c.serviceClient.Get(utils.LHNameSpace, svcName, metav1.GetOptions{})
	if err != nil {
		logrus.Errorf("Failed to get service %s: %v", svcName, err)
		return nil, err
	}
	if service.Spec.ClusterIP != corev1.ClusterIPNone {
		logrus.Infof("Skip update with endpointslice change event because service %s is not ClusterIPNone", service.Name)
		return nil, nil
	}

	logrus.Debugf("EndpointSlice %s (service %s) has address type %s", endpointSlice.Name, svcName, endpointSlice.AddressType)

	// A dual-stack service has one EndpointSlice per IP family, so a change to one slice
	// (e.g. the IPv6 one) must not clobber the address already picked from its sibling.
	// List all slices for the service rather than relying solely on the slice that triggered
	// this event.
	siblingSlices, err := c.EndpointSliceCache.List(utils.LHNameSpace, labels.SelectorFromSet(labels.Set{
		discoveryv1.LabelServiceName: svcName,
	}))
	if err != nil {
		logrus.Errorf("Failed to list endpointslices of service %s: %v", svcName, err)
		return nil, err
	}

	networkFSCpy := networkFS.DeepCopy()
	address, addressIPv6 := firstReadyAddresses(siblingSlices)
	// Fall back to the IPv6 address only when no IPv4 slice is ready yet, so an IPv6-only
	// or not-yet-converged dual-stack rollout still gets a usable endpoint.
	if address == "" {
		address = addressIPv6
	}
	if address == "" {
		networkFSCpy.Status.Endpoint = ""
		networkFSCpy.Status.EndpointIPv6 = ""
		networkFSCpy.Status.Status = networkfsv1.EndpointStatusNotReady
		networkFSCpy.Status.Type = networkfsv1.NetworkFSTypeNFS
		networkFSCpy.Status.State = networkfsv1.NetworkFSStateEnabling
		conds := networkfsv1.NetworkFSCondition{
			Type:               networkfsv1.ConditionTypeNotReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "Endpoint is not ready",
			Message:            "EndpointSlice did not contain any ready address",
		}
		networkFSCpy.Status.NetworkFSConds = utils.UpdateNetworkFSConds(networkFSCpy.Status.NetworkFSConds, conds)
	} else {
		if networkFSCpy.Status.Endpoint != address {
			changedMsg := "Endpoint address is initialized with " + address
			if networkFSCpy.Status.Endpoint != "" {
				changedMsg = "Endpoint address is changed, previous address is " + networkFSCpy.Status.Endpoint
			}
			conds := networkfsv1.NetworkFSCondition{
				Type:               networkfsv1.ConditionTypeEndpointChanged,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             "Endpoint is changed",
				Message:            changedMsg,
			}
			networkFSCpy.Status.NetworkFSConds = utils.UpdateNetworkFSConds(networkFSCpy.Status.NetworkFSConds, conds)
		}
		networkFSCpy.Status.Endpoint = address
		networkFSCpy.Status.EndpointIPv6 = addressIPv6
		networkFSCpy.Status.Status = networkfsv1.EndpointStatusReady
		networkFSCpy.Status.Type = networkfsv1.NetworkFSTypeNFS
		networkFSCpy.Status.State = networkfsv1.NetworkFSStateEnabling
		conds := networkfsv1.NetworkFSCondition{
			Type:               networkfsv1.ConditionTypeReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "Endpoint is ready",
			Message:            "EndpointSlice contains the corresponding address",
		}
		networkFSCpy.Status.NetworkFSConds = utils.UpdateNetworkFSConds(networkFSCpy.Status.NetworkFSConds, conds)
	}

	if !reflect.DeepEqual(networkFS, networkFSCpy) {
		if _, err := c.NetworkFilsystems.UpdateStatus(networkFSCpy); err != nil {
			logrus.Errorf("Failed to update networkFS %s: %v", networkFS.Name, err)
			return nil, err
		}
	}

	return nil, nil
}

// firstReadyAddresses returns the IPv4 and IPv6 addresses of the first ready endpoint found
// across endpointSlices. A nil Ready condition is treated as ready, as defined by the
// EndpointSlice API. A dual-stack service has one EndpointSlice per IP family, so the pick
// is merged across all of them rather than assuming a single slice carries both families.
func firstReadyAddresses(endpointSlices []*discoveryv1.EndpointSlice) (string, string) {
	var ipv4, ipv6 string
	for _, endpointSlice := range endpointSlices {
		for _, endpoint := range endpointSlice.Endpoints {
			if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
				continue
			}
			if len(endpoint.Addresses) == 0 {
				continue
			}
			v4, v6 := utils.SelectEndpointAddresses(endpoint.Addresses)
			if ipv4 == "" {
				ipv4 = v4
			}
			if ipv6 == "" {
				ipv6 = v6
			}
			break
		}
	}
	return ipv4, ipv6
}

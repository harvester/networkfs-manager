package endpoint

import (
	"context"
	"reflect"
	"strings"

	ctlcorev1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkfsv1 "github.com/harvester/networkfs-manager/pkg/apis/harvesterhci.io/v1beta1"
	ctlntefsv1 "github.com/harvester/networkfs-manager/pkg/generated/controllers/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/utils"
)

type Controller struct {
	namespace string
	nodeName  string

	EndpointCache     ctlcorev1.EndpointsCache
	Endpoints         ctlcorev1.EndpointsController
	NetworkFSCache    ctlntefsv1.NetworkFilesystemCache
	NetworkFilsystems ctlntefsv1.NetworkFilesystemController

	serviceClient ctlcorev1.ServiceController
}

const (
	netFSEndpointHandlerName = "harvester-netfs-endpoint-handler"
)

// Register register the longhorn node CRD controller
func Register(ctx context.Context, endpoint ctlcorev1.EndpointsController, netfilesystems ctlntefsv1.NetworkFilesystemController, serviceClient ctlcorev1.ServiceController, opt *utils.Option) error {

	c := &Controller{
		namespace:         opt.Namespace,
		nodeName:          opt.NodeName,
		Endpoints:         endpoint,
		EndpointCache:     endpoint.Cache(),
		NetworkFilsystems: netfilesystems,
		NetworkFSCache:    netfilesystems.Cache(),
		serviceClient:     serviceClient,
	}

	c.Endpoints.OnChange(ctx, netFSEndpointHandlerName, c.OnEndpointChange)
	return nil
}

// OnChange watch the node CR on change and sync up to block device CR
func (c *Controller) OnEndpointChange(_ string, endpoint *corev1.Endpoints) (*corev1.Endpoints, error) {
	if endpoint == nil || endpoint.DeletionTimestamp != nil {
		logrus.Infof("Skip this round because endpoint is deleted or deleting")
		return nil, nil
	}

	// we only care about the endpoint with name prefix "pvc-"
	if !strings.HasPrefix(endpoint.Name, "pvc-") {
		return nil, nil
	}

	logrus.Infof("Handling endpoint %s change event", endpoint.Name)
	networkFS, err := c.NetworkFilsystems.Get(c.namespace, endpoint.Name, metav1.GetOptions{})
	if err != nil {
		logrus.Errorf("Failed to get networkFS %s: %v", endpoint.Name, err)
		return nil, err
	}

	// only update when the networkfilesystem is enabled.
	if networkFS.Spec.DesiredState != networkfsv1.NetworkFSStateEnabled {
		logrus.Infof("Skip update with endpoint change event because networkfilesystem %s is not enabled", networkFS.Name)
		return nil, nil
	}

	// skip update if the service.Spec.ClusterIP is not ClusterIPNone (means the we depends on service)
	service, err := c.serviceClient.Get(utils.LHNameSpace, endpoint.Name, metav1.GetOptions{})
	if err != nil {
		logrus.Errorf("Failed to get service %s: %v", endpoint.Name, err)
		return nil, err
	}
	if service.Spec.ClusterIP != corev1.ClusterIPNone {
		logrus.Infof("Skip update with endpoint change event because service %s is not ClusterIPNone", service.Name)
		return nil, nil
	}

	networkFSCpy := networkFS.DeepCopy()
	if len(endpoint.Subsets) == 0 || len(endpoint.Subsets[0].Addresses) == 0 {
		networkFSCpy.Status.Endpoint = ""
		networkFSCpy.Status.Status = networkfsv1.EndpointStatusNotReady
		networkFSCpy.Status.Type = networkfsv1.NetworkFSTypeNFS
		networkFSCpy.Status.State = networkfsv1.NetworkFSStateEnabling
		conds := networkfsv1.NetworkFSCondition{
			Type:               networkfsv1.ConditionTypeNotReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "Endpoint is not ready",
			Message:            "Endpoint did not contain the corresponding address",
		}
		networkFSCpy.Status.NetworkFSConds = utils.UpdateNetworkFSConds(networkFSCpy.Status.NetworkFSConds, conds)
	} else {
		if networkFSCpy.Status.Endpoint != endpoint.Subsets[0].Addresses[0].IP {
			changedMsg := "Endpoint address is initialized with " + endpoint.Subsets[0].Addresses[0].IP
			if changedMsg != "" {
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
		networkFSCpy.Status.Endpoint = endpoint.Subsets[0].Addresses[0].IP
		networkFSCpy.Status.Status = networkfsv1.EndpointStatusReady
		networkFSCpy.Status.Type = networkfsv1.NetworkFSTypeNFS
		networkFSCpy.Status.State = networkfsv1.NetworkFSStateEnabling
		conds := networkfsv1.NetworkFSCondition{
			Type:               networkfsv1.ConditionTypeReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "Endpoint is ready",
			Message:            "Endpoint contains the corresponding address",
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

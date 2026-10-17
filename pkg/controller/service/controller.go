package service

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

	ServiceCache      ctlcorev1.ServiceCache
	Services          ctlcorev1.ServiceController
	NetworkFSCache    ctlntefsv1.NetworkFilesystemCache
	NetworkFilsystems ctlntefsv1.NetworkFilesystemController
}

const (
	netFSEndpointHandlerName = "harvester-netfs-service-handler"
)

// Register register the longhorn node CRD controller
func Register(ctx context.Context, services ctlcorev1.ServiceController, netfilesystems ctlntefsv1.NetworkFilesystemController, opt *utils.Option) error {

	c := &Controller{
		namespace:         opt.Namespace,
		nodeName:          opt.NodeName,
		Services:          services,
		ServiceCache:      services.Cache(),
		NetworkFilsystems: netfilesystems,
		NetworkFSCache:    netfilesystems.Cache(),
	}

	c.Services.OnChange(ctx, netFSEndpointHandlerName, c.OnServicesChange)
	return nil
}

// OnServicesChange watch the services CR on change and sync up to networkfilesystem CR
func (c *Controller) OnServicesChange(_ string, service *corev1.Service) (*corev1.Service, error) {
	if service == nil || service.DeletionTimestamp != nil {
		logrus.Infof("Skip this round because service is deleted or deleting")
		return nil, nil
	}

	// we only care about the endpoint with name prefix "pvc-"
	if !strings.HasPrefix(service.Name, "pvc-") {
		return nil, nil
	}

	logrus.Infof("Handling service %s change event", service.Name)
	networkFS, err := c.NetworkFilsystems.Get(c.namespace, service.Name, metav1.GetOptions{})
	if err != nil {
		logrus.Errorf("Failed to get networkFS %s: %v", service.Name, err)
		return nil, err
	}

	// only update when the networkfilesystem is enabled.
	if networkFS.Spec.DesiredState != networkfsv1.NetworkFSStateEnabled {
		logrus.Infof("Skip update with endpoint change event because networkfilesystem %s is not enabled", networkFS.Name)
		return nil, nil
	}

	// means we depends on the endpoint, skip the update
	if service.Spec.ClusterIP == corev1.ClusterIPNone {
		return nil, nil
	}

	clusterIPs := service.Spec.ClusterIPs
	if len(clusterIPs) == 0 && service.Spec.ClusterIP != "" {
		clusterIPs = []string{service.Spec.ClusterIP}
	}
	endpoint, endpointIPv6 := utils.SelectEndpointAddresses(clusterIPs)
	if endpoint == "" {
		endpoint = endpointIPv6
	}

	networkFSCpy := networkFS.DeepCopy()
	// Not updated, skip the update (we will get a new service CR later)
	if endpoint == "" {
		networkFSCpy.Status.Endpoint = ""
		networkFSCpy.Status.EndpointIPv6 = ""
		networkFSCpy.Status.Status = networkfsv1.EndpointStatusNotReady
		networkFSCpy.Status.Type = networkfsv1.NetworkFSTypeNFS
		networkFSCpy.Status.State = networkfsv1.NetworkFSStateEnabling
		conds := networkfsv1.NetworkFSCondition{
			Type:               networkfsv1.ConditionTypeNotReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "Service is not ready",
			Message:            "Service did not contain the corresponding address",
		}
		networkFSCpy.Status.NetworkFSConds = utils.UpdateNetworkFSConds(networkFSCpy.Status.NetworkFSConds, conds)
	} else {
		if networkFSCpy.Status.Endpoint != endpoint {
			changedMsg := "Endpoint address is initialized with " + endpoint
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
		networkFSCpy.Status.Endpoint = endpoint
		networkFSCpy.Status.EndpointIPv6 = endpointIPv6
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

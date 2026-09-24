package service

import (
	"context"

	ctlcorev1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	"github.com/harvester/networkfs-manager/pkg/controller/endpoint"
	networkfsstatus "github.com/harvester/networkfs-manager/pkg/controller/networkfilesystem/status"
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
	if !shouldHandleService(service) {
		return nil, nil
	}

	volumeName, selection, managed, err := endpoint.ResolveService(c.Services, service.Namespace, service)
	if err != nil {
		logrus.Errorf("Failed to resolve endpoint service for networkFS %s: %v", volumeName, err)
		return nil, err
	}
	if !managed {
		return nil, nil
	}

	logrus.Infof("Handling service %s change event for network filesystem %s", service.Name, volumeName)
	if selection.Source == endpoint.SourceEndpointSlice {
		return nil, nil
	}
	if err := networkfsstatus.ReconcileEndpoint(c.NetworkFilsystems, c.namespace, volumeName, selection); err != nil {
		logrus.Errorf("Failed to reconcile endpoint for networkFS %s: %v", volumeName, err)
		return nil, err
	}

	return nil, nil
}

func shouldHandleService(service *corev1.Service) bool {
	return service.Namespace == utils.LHNameSpace ||
		service.Labels[endpoint.RWXVolumeServiceLabel] != ""
}

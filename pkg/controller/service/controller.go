package service

import (
	"context"

	ctlcorev1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	"github.com/harvester/networkfs-manager/pkg/controller/endpoint"
	ctlntefsv1 "github.com/harvester/networkfs-manager/pkg/generated/controllers/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/utils"
)

type Controller struct {
	namespace string

	Services          ctlcorev1.ServiceController
	NetworkFilsystems ctlntefsv1.NetworkFilesystemController
}

const (
	netFSEndpointHandlerName = "harvester-netfs-service-handler"
)

// Register watches Service changes that affect NetworkFilesystem endpoints.
func Register(
	ctx context.Context,
	services ctlcorev1.ServiceController,
	netfilesystems ctlntefsv1.NetworkFilesystemController,
	opt *utils.Option,
) error {
	c := &Controller{
		namespace:         opt.Namespace,
		Services:          services,
		NetworkFilsystems: netfilesystems,
	}

	c.Services.OnChange(ctx, netFSEndpointHandlerName, c.OnServicesChange)
	return nil
}

// OnServicesChange queues the volume so endpoint selection runs in its reconcile.
func (c *Controller) OnServicesChange(_ string, service *corev1.Service) (*corev1.Service, error) {
	if service == nil || service.DeletionTimestamp != nil {
		logrus.Infof("Skip this round because service is deleted or deleting")
		return nil, nil
	}
	if !shouldHandleService(service) {
		return nil, nil
	}

	volumeName, managed := endpoint.VolumeName(service)
	if !managed {
		return nil, nil
	}

	c.NetworkFilsystems.Enqueue(c.namespace, volumeName)
	return nil, nil
}

func shouldHandleService(service *corev1.Service) bool {
	return service.Namespace == utils.LHNameSpace
}

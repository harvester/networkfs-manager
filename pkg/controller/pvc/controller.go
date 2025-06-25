package pvc

import (
	"context"

	ctlcorev1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ctlntefsv1 "github.com/harvester/networkfs-manager/pkg/generated/controllers/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/utils"
)

type Controller struct {
	namespace string
	nodeName  string

	PVCs              ctlcorev1.PersistentVolumeClaimController
	NetworkFSCache    ctlntefsv1.NetworkFilesystemCache
	NetworkFilsystems ctlntefsv1.NetworkFilesystemController
}

const (
	netFSEndpointHandlerName = "harvester-netfs-endpoint-handler"
)

// Register register the longhorn node CRD controller
func Register(ctx context.Context, pvcs ctlcorev1.PersistentVolumeClaimController, netfilesystems ctlntefsv1.NetworkFilesystemController, opt *utils.Option) error {

	c := &Controller{
		namespace:         opt.Namespace,
		nodeName:          opt.NodeName,
		PVCs:              pvcs,
		NetworkFilsystems: netfilesystems,
		NetworkFSCache:    netfilesystems.Cache(),
	}

	c.PVCs.OnChange(ctx, netFSEndpointHandlerName, c.OnPVCChange)
	return nil
}

func (c *Controller) OnPVCChange(_ string, pvc *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, error) {
	if pvc == nil || pvc.DeletionTimestamp != nil {
		return nil, nil
	}

	logrus.Infof("Handling pvc %s/%s change event", pvc.Namespace, pvc.Name)
	networkFS, err := c.NetworkFilsystems.Get(c.namespace, pvc.Spec.VolumeName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return nil, nil
	}

	if err != nil {
		logrus.Infof("Failed to get networkFS for pvc %s/%s: %v", pvc.Namespace, pvc.Name, err)
		return nil, err
	}

	if utils.PVCNeedExpand(pvc) {
		logrus.Infof("Enqueue networkFS %s for pvc %s/%s", networkFS.Name, pvc.Namespace, pvc.Name)
		c.NetworkFilsystems.Enqueue(c.namespace, networkFS.Name)
		return pvc, nil
	}

	if utils.NetFSInExpanding(networkFS) {
		logrus.Infof("PVC %s/%s expand completes, enqueue netfs %s", pvc.Namespace, pvc.Name, networkFS.Name)
		c.NetworkFilsystems.Enqueue(c.namespace, networkFS.Name)
		return pvc, nil
	}

	logrus.Infof("PVC %s/%s not requires expand", pvc.Namespace, pvc.Name)
	return pvc, nil
}

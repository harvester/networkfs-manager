package sharemanager

import (
	"context"
	"reflect"

	longhornv1 "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkfsv1 "github.com/harvester/networkfs-manager/pkg/apis/harvesterhci.io/v1beta1"
	ctlntefsv1 "github.com/harvester/networkfs-manager/pkg/generated/controllers/harvesterhci.io/v1beta1"
	ctllonghornv1 "github.com/harvester/networkfs-manager/pkg/generated/controllers/longhorn.io/v1beta2"
	"github.com/harvester/networkfs-manager/pkg/utils"
)

type Controller struct {
	namespace string
	nodeName  string

	ShareManagerCache ctllonghornv1.ShareManagerCache
	ShareManagers     ctllonghornv1.ShareManagerController
	NetworkFSCache    ctlntefsv1.NetworkFilesystemCache
	NetworkFilsystems ctlntefsv1.NetworkFilesystemController
}

const (
	netFSEndpointHandlerName = "harvester-netfs-sharemanager-handler"
)

// Register register the longhorn node CRD controller
func Register(ctx context.Context, sharemanager ctllonghornv1.ShareManagerController, netfilesystems ctlntefsv1.NetworkFilesystemController, opt *utils.Option) error {

	c := &Controller{
		namespace:         opt.Namespace,
		nodeName:          opt.NodeName,
		ShareManagers:     sharemanager,
		ShareManagerCache: sharemanager.Cache(),
		NetworkFilsystems: netfilesystems,
		NetworkFSCache:    netfilesystems.Cache(),
	}

	c.ShareManagers.OnChange(ctx, netFSEndpointHandlerName, c.OnShareManagerChange)
	return nil
}

// OnChange watch the node CR on change and sync up to block device CR
func (c *Controller) OnShareManagerChange(_ string, sharemanager *longhornv1.ShareManager) (*longhornv1.ShareManager, error) {
	if sharemanager == nil || sharemanager.DeletionTimestamp != nil {
		logrus.Infof("Skip this round because sharemanager is deleted or deleting")
		return nil, nil
	}

	// only handle the stopped sharemanager (for update the networkfs status)
	if sharemanager.Status.State != longhornv1.ShareManagerStateStopped {
		return nil, nil
	}

	logrus.Infof("Handling sharemanager %s change event", sharemanager.Name)
	networkFS, err := c.NetworkFilsystems.Get(c.namespace, sharemanager.Name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		logrus.Errorf("Failed to get networkFS %s: %v", sharemanager.Name, err)
		return nil, err
	}
	if apierrors.IsNotFound(err) {
		logrus.Infof("Skip update with sharemanager change event because there is no corresponding NetworkFS %v", sharemanager.Name)
		return nil, nil
	}

	// already disabled, return
	if networkFS.Status.State == networkfsv1.NetworkFSStateDisabled {
		logrus.Infof("Skip update with sharemanager change event because networkfilesystem %s is not enabled", networkFS.Name)
		return nil, nil
	}

	// only handle it on the networkfs is disabled.
	if networkFS.Spec.DesiredState != networkfsv1.NetworkFSStateDisabled || networkFS.Status.State != networkfsv1.NetworkFSStateDisabling {
		return nil, nil
	}

	networkFSCpy := networkFS.DeepCopy()
	networkFSCpy.Status.State = networkfsv1.NetworkFSStateDisabled
	networkFSCpy.Status.Endpoint = ""
	networkFSCpy.Status.EndpointIPv6 = ""
	networkFSCpy.Status.Status = networkfsv1.EndpointStatusNotReady
	networkFSCpy.Status.Type = networkfsv1.NetworkFSTypeNFS
	networkFSCpy.Status.MountOpts = ""
	conds := networkfsv1.NetworkFSCondition{
		Type:               networkfsv1.ConditionTypeNotReady,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             "ShareManager is stopped",
		Message:            "ShareManager is stopped, means the networkfs is disabled",
	}
	networkFSCpy.Status.NetworkFSConds = utils.UpdateNetworkFSConds(networkFSCpy.Status.NetworkFSConds, conds)

	if !reflect.DeepEqual(networkFS, networkFSCpy) {
		if _, err := c.NetworkFilsystems.UpdateStatus(networkFSCpy); err != nil {
			logrus.Errorf("Failed to update networkFS %s: %v", networkFS.Name, err)
			return nil, err
		}
	}

	return nil, nil
}

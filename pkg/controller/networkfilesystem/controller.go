package networkfilesystem

import (
	"context"
	"fmt"
	"reflect"

	longhornv2 "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
	lhclientset "github.com/longhorn/longhorn-manager/k8s/pkg/client/clientset/versioned"
	ctlv1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkfsv1 "github.com/harvester/networkfs-manager/pkg/apis/harvesterhci.io/v1beta1"
	ctlntefsv1 "github.com/harvester/networkfs-manager/pkg/generated/controllers/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/utils"
)

type Controller struct {
	namespace string
	nodeName  string

	coreClient        ctlv1.Interface
	lhClient          *lhclientset.Clientset
	endpointsClient   ctlv1.EndpointsController
	NetworkFSCache    ctlntefsv1.NetworkFilesystemCache
	NetworkFilsystems ctlntefsv1.NetworkFilesystemController
}

const (
	netFSHandlerName = "harvester-network-filesystem-handler"
)

// Register register the longhorn node CRD controller
func Register(ctx context.Context, coreClient ctlv1.Interface, lhClient *lhclientset.Clientset, endpoints ctlv1.EndpointsController, netfilesystems ctlntefsv1.NetworkFilesystemController, opt *utils.Option) error {

	c := &Controller{
		namespace:         opt.Namespace,
		nodeName:          opt.NodeName,
		coreClient:        coreClient,
		lhClient:          lhClient,
		endpointsClient:   endpoints,
		NetworkFilsystems: netfilesystems,
		NetworkFSCache:    netfilesystems.Cache(),
	}

	c.NetworkFilsystems.OnChange(ctx, netFSHandlerName, c.OnNetworkFSChange)
	return nil
}

func (c *Controller) OnNetworkFSChange(_ string, networkFS *networkfsv1.NetworkFilesystem) (*networkfsv1.NetworkFilesystem, error) {
	if networkFS == nil || networkFS.DeletionTimestamp != nil {
		logrus.Infof("Skip this round because the network filesystem is deleting")
		return nil, nil
	}
	logrus.Infof("Handling network filesystem %s change event", networkFS.Name)

	if networkFS.Spec.DesiredState == networkFS.Status.State {
		logrus.Infof("Skip this round because the network filesystem %s is already in desired state %s", networkFS.Name, networkFS.Spec.DesiredState)
		return nil, nil
	}

	if networkFS.Status.State == "" {
		// means empty Status, init first
		logrus.Infof("Init network filesystem %s status", networkFS.Name)
		networkFSCpy := networkFS.DeepCopy()
		status := networkfsv1.NetworkFSStatus{
			State:  networkfsv1.NetworkFSStateDisabled,
			Status: networkfsv1.EndpointStatusUnknown,
			Type:   networkfsv1.NetworkFSTypeNFS,
		}
		networkFSCpy.Status = status
		return c.NetworkFilsystems.UpdateStatus(networkFSCpy)
	}

	// Disabled -> Enabling -> Enabled -> Disabling -> Disabled
	switch networkFS.Spec.DesiredState {
	case networkfsv1.NetworkFSStateEnabled:
		return c.enableNetworkFS(networkFS)
	case networkfsv1.NetworkFSStateDisabled:
		return c.disableNetworkFS(networkFS)
	default:
		logrus.Errorf("Unknown desired state %s for network filesystem %s", networkFS.Spec.DesiredState, networkFS.Name)
	}

	return nil, nil
}

func (c *Controller) disableNetworkFS(networkFS *networkfsv1.NetworkFilesystem) (*networkfsv1.NetworkFilesystem, error) {
	logrus.Infof("Disable network filesystem %s", networkFS.Name)

	if !isDisabling(networkFS) {
		if err := c.updateLHVolumeAttachment(networkFS, false); err != nil {
			return nil, err
		}
		networkFSCpy := networkFS.DeepCopy()
		networkFSCpy.Status.State = networkfsv1.NetworkFSStateDisabling
		if !reflect.DeepEqual(networkFS, networkFSCpy) {
			return c.NetworkFilsystems.UpdateStatus(networkFSCpy)
		}
	}
	return nil, nil
}

func (c *Controller) enableNetworkFS(networkFS *networkfsv1.NetworkFilesystem) (*networkfsv1.NetworkFilesystem, error) {
	logrus.Infof("Enable network filesystem %s", networkFS.Name)

	// After update the LH volume attachment, we need to wait LH share manager provision.
	lhShareMgr, err := c.lhClient.LonghornV1beta2().ShareManagers(utils.LHNameSpace).Get(context.Background(), networkFS.Name, metav1.GetOptions{})
	if err != nil {
		logrus.Errorf("Failed to get share manager %s: %v", networkFS.Name, err)
		return nil, err
	}

	if !isEnabling(networkFS) && lhShareMgr.Status.State == longhornv2.ShareManagerStateStopped {
		// enable the network filesystem need to wait the previous operation (disable) to finish
		if networkFS.Status.State != networkfsv1.NetworkFSStateDisabled {
			logrus.Infof("Wait the previous operation (disable) to finish")
			return nil, fmt.Errorf("wait the previous operation (disable) to finish on %v", networkFS.Name)
		}
		logrus.Infof("Endpoint %s is not ready, update lhVA to trigger export endpoint", networkFS.Name)
		if err := c.updateLHVolumeAttachment(networkFS, true); err != nil {
			return nil, err
		}
		networkFSNew := setNetworkFSToEnabling(networkFS, networkfsv1.NetworkFSTypeNFS)
		if !reflect.DeepEqual(networkFS, networkFSNew) {
			return c.NetworkFilsystems.UpdateStatus(networkFSNew)
		}
	}

	if lhShareMgr.Status.State != longhornv2.ShareManagerStateRunning {
		// check the LHVA again, we encounter the corner case that the lhva is cleaned up
		// when we disable/enable the network filesystem very soon.
		lhva, err := c.lhClient.LonghornV1beta2().VolumeAttachments(utils.LHNameSpace).Get(context.Background(), networkFS.Name, metav1.GetOptions{})
		if err != nil {
			logrus.Errorf("Failed to get Longhorn volume attachment %s: %v", networkFS.Name, err)
			return nil, err
		}
		if len(lhva.Spec.AttachmentTickets) == 0 {
			// this will reset the network filesystem status to disabled
			// after reconcile, we will retry to enable the network filesystem
			logrus.Infof("The LHVA %s has no attachment tickets, reset the network filesystem status for retry", networkFS.Name)
			networkfsNew := setNetworkFSToDefault(networkFS)
			if !reflect.DeepEqual(networkFS, networkfsNew) {
				return c.NetworkFilsystems.UpdateStatus(networkfsNew)
			}
		}
		logrus.Infof("Wait the share manager %s to be running", networkFS.Name)
		return nil, fmt.Errorf("wait the share manager %s to be running", networkFS.Name)
	}

	// LH RWX volume endpoint should only have one address and one port
	netFSEndpoint := ""
	service, err := c.coreClient.Service().Get(utils.LHNameSpace, networkFS.Name, metav1.GetOptions{})
	if err != nil {
		logrus.Errorf("Failed to get service %s: %v", networkFS.Name, err)
		return nil, err
	}
	if service.Spec.ClusterIP != corev1.ClusterIPNone {
		// means we depends on the service
		if service.Spec.ClusterIP == "" {
			// first init, service controller will update
			logrus.Infof("Skip update on networkfs controller with the first time service init")
			return nil, nil
		}
		netFSEndpoint = service.Spec.ClusterIP
	} else {
		endpoint, err := c.endpointsClient.Get(utils.LHNameSpace, networkFS.Name, metav1.GetOptions{})
		if err != nil && !errors.IsNotFound(err) {
			logrus.Errorf("Failed to get endpoint %s: %v", networkFS.Name, err)
		}
		if len(endpoint.Subsets) == 0 {
			logrus.Infof("Endpoint %s has no subsets (not ready), skip this round!", networkFS.Name)
			return nil, nil
		}
		if len(endpoint.Subsets) > 1 || len(endpoint.Subsets[0].Addresses) > 1 || len(endpoint.Subsets[0].Ports) > 1 {
			return nil, fmt.Errorf("endpoint %s has more than one subSets", networkFS.Name)
		}
		if endpoint.Subsets[0].Ports[0].Name != "nfs" {
			return nil, fmt.Errorf("endpoint %s has no nfs port", networkFS.Name)
		}
		netFSEndpoint = endpoint.Subsets[0].Addresses[0].IP
	}

	pv, err := c.coreClient.PersistentVolume().Get(networkFS.Name, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		logrus.Errorf("Failed to get persistent volume %s: %v", networkFS.Name, err)
		return nil, err
	}
	opts := ""
	if _, found := pv.Spec.CSI.VolumeAttributes["nfsOptions"]; found {
		opts = pv.Spec.CSI.VolumeAttributes["nfsOptions"]
	}
	networkFSNew := setNetworkFSToEnabled(networkFS, networkfsv1.NetworkFSTypeNFS, netFSEndpoint, opts)
	// update network filesystem status
	return c.NetworkFilsystems.UpdateStatus(networkFSNew)
}

func (c *Controller) updateLHVolumeAttachment(networkFS *networkfsv1.NetworkFilesystem, attach bool) error {
	logrus.Infof("Update Longhorn volume attachment for network filesystem %s, attach: %v", networkFS.Name, attach)

	// get Longhorn volume attachment
	lhva, err := c.lhClient.LonghornV1beta2().VolumeAttachments(utils.LHNameSpace).Get(context.Background(), networkFS.Name, metav1.GetOptions{})
	if err != nil {
		logrus.Errorf("Failed to get Longhorn volume attachment %s: %v", networkFS.Name, err)
		return err
	}

	if attach {
		return c.doAttachLHVolumeAttachment(networkFS, lhva)
	}
	return c.doDeattachLHVolumeAttachment(networkFS, lhva)
}

func (c *Controller) doDeattachLHVolumeAttachment(networkFS *networkfsv1.NetworkFilesystem, lhva *longhornv2.VolumeAttachment) error {
	lhvaCpy := lhva.DeepCopy()
	lhvaCpy.Spec.AttachmentTickets = map[string]*longhornv2.AttachmentTicket{}
	if !reflect.DeepEqual(lhva, lhvaCpy) {
		if _, err := c.lhClient.LonghornV1beta2().VolumeAttachments(utils.LHNameSpace).Update(context.Background(), lhvaCpy, metav1.UpdateOptions{}); err != nil {
			logrus.Errorf("Failed to update Longhorn volume attachment %s: %v", networkFS.Name, err)
			return err
		}
	}
	return nil
}

func (c *Controller) doAttachLHVolumeAttachment(networkFS *networkfsv1.NetworkFilesystem, lhva *longhornv2.VolumeAttachment) error {
	lhvaCpy := lhva.DeepCopy()
	lhvaCpy.Spec.AttachmentTickets = map[string]*longhornv2.AttachmentTicket{}
	nodeID := ""
	if networkFS.Spec.PreferredNode != "" {
		nodeID = networkFS.Spec.PreferredNode
	}
	csiTicketID := fmt.Sprintf("csi-%s", networkFS.Name)
	shareMgrTicketID := fmt.Sprintf("share-manager-controller-%s", networkFS.Name)

	// RWX volume should have two attachment tickets (CSI and share-manager)
	attachmentTicketCSI, ok := lhva.Spec.AttachmentTickets[csiTicketID]
	if !ok {
		// Create new one
		attachmentTicketCSI = &longhornv2.AttachmentTicket{
			ID:     csiTicketID,
			Type:   longhornv2.AttacherTypeCSIAttacher,
			NodeID: nodeID,
			Parameters: map[string]string{
				longhornv2.AttachmentParameterDisableFrontend: "false",
			},
		}
	}
	lhvaCpy.Spec.AttachmentTickets[csiTicketID] = attachmentTicketCSI

	attachmentTicketSM, ok := lhva.Spec.AttachmentTickets[shareMgrTicketID]
	if !ok {
		// Create new one
		attachmentTicketSM = &longhornv2.AttachmentTicket{
			ID:     shareMgrTicketID,
			Type:   longhornv2.AttacherTypeShareManagerController,
			NodeID: nodeID,
			Parameters: map[string]string{
				longhornv2.AttachmentParameterDisableFrontend: "false",
			},
		}
	}
	lhvaCpy.Spec.AttachmentTickets[shareMgrTicketID] = attachmentTicketSM

	if !reflect.DeepEqual(lhva, lhvaCpy) {
		if _, err := c.lhClient.LonghornV1beta2().VolumeAttachments(utils.LHNameSpace).Update(context.Background(), lhvaCpy, metav1.UpdateOptions{}); err != nil {
			logrus.Errorf("Failed to update Longhorn volume attachment %s: %v", networkFS.Name, err)
			return err
		}
	}
	return nil
}

func isEnabling(networkFS *networkfsv1.NetworkFilesystem) bool {
	return networkFS.Status.State == networkfsv1.NetworkFSStateEnabling
}

func isDisabling(networkFS *networkfsv1.NetworkFilesystem) bool {
	return networkFS.Status.State == networkfsv1.NetworkFSStateDisabling
}

func setNetworkFSToDefault(networkFS *networkfsv1.NetworkFilesystem) *networkfsv1.NetworkFilesystem {
	networkFSCpy := networkFS.DeepCopy()
	setNetworkFSStatus(networkFSCpy, networkfsv1.NetworkFSStateDisabled, networkfsv1.EndpointStatusNotReady)
	return networkFSCpy
}

func setNetworkFSToEnabling(networkFS *networkfsv1.NetworkFilesystem, targetNetFS string) *networkfsv1.NetworkFilesystem {
	networkFSCpy := networkFS.DeepCopy()
	setNetworkFSStatus(networkFSCpy, networkfsv1.NetworkFSStateEnabling, networkfsv1.EndpointStatusNotReady)
	setNetworkFSType(networkFSCpy, targetNetFS)
	return networkFSCpy
}

func setNetworkFSToEnabled(networkFS *networkfsv1.NetworkFilesystem, targetNetFS, endpoint, opts string) *networkfsv1.NetworkFilesystem {
	networkFSCpy := networkFS.DeepCopy()
	setNetworkFSStatus(networkFSCpy, networkfsv1.NetworkFSStateEnabled, networkfsv1.EndpointStatusReady)
	setNetworkFSType(networkFSCpy, targetNetFS)
	setNetworkFSEndpoint(networkFSCpy, endpoint)
	setNetworkFSOpts(networkFSCpy, opts)
	conds := networkfsv1.NetworkFSCondition{
		Type:               networkfsv1.ConditionTypeEndpointChanged,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             "Endpoint is changed",
		Message:            fmt.Sprintf("Endpoint is changed to %s", endpoint),
	}
	networkFSCpy.Status.NetworkFSConds = utils.UpdateNetworkFSConds(networkFSCpy.Status.NetworkFSConds, conds)
	return networkFSCpy
}

func setNetworkFSStatus(networkFS *networkfsv1.NetworkFilesystem,
	state networkfsv1.NetworkFSState,
	status networkfsv1.EndpointStatus) {
	networkFS.Status.State = state
	networkFS.Status.Status = status
}

func setNetworkFSType(networkFS *networkfsv1.NetworkFilesystem, targetNetFS string) {
	networkFS.Status.Type = targetNetFS
}

func setNetworkFSEndpoint(networkFS *networkfsv1.NetworkFilesystem, endpoint string) {
	networkFS.Status.Endpoint = endpoint
}

func setNetworkFSOpts(networkFS *networkfsv1.NetworkFilesystem, opts string) {
	networkFS.Status.MountOpts = opts
}

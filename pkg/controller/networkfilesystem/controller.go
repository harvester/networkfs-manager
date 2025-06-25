package networkfilesystem

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	longhornv2 "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
	lhclientset "github.com/longhorn/longhorn-manager/k8s/pkg/client/clientset/versioned"
	ctlv1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/pointer"

	networkfsv1 "github.com/harvester/networkfs-manager/pkg/apis/harvesterhci.io/v1beta1"
	ctlntefsv1 "github.com/harvester/networkfs-manager/pkg/generated/controllers/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/utils"
)

const (
	mntPathPrefix = "/data/"
	expanderName  = "expander"
	expanderCmd   = "sleep"
	expanderArg   = "300"
)

type Controller struct {
	namespace string
	nodeName  string

	clientSet         *kubernetes.Clientset
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
func Register(ctx context.Context, clientSet *kubernetes.Clientset, coreClient ctlv1.Interface,
	lhClient *lhclientset.Clientset, endpoints ctlv1.EndpointsController,
	netfilesystems ctlntefsv1.NetworkFilesystemController, opt *utils.Option) error {

	c := &Controller{
		namespace:         opt.Namespace,
		nodeName:          opt.NodeName,
		clientSet:         clientSet,
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
		return c.checkAndProcessExpand(networkFS)
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

func (c *Controller) checkAndProcessExpand(networkFS *networkfsv1.NetworkFilesystem) (*networkfsv1.NetworkFilesystem, error) {
	pvc, err := c.getPVC(networkFS)
	if err != nil {
		return nil, err
	}

	if !isEnabled(networkFS) {
		return nil, nil
	}

	if utils.PVCNeedExpand(pvc) {
		return c.expandNetworkFS(networkFS, pvc)
	}
	return c.completeExpandNetworkFS(networkFS, pvc)
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

func (c *Controller) getClusterRepoImage() string {
	deployContent, err := c.clientSet.AppsV1().Deployments("cattle-system").Get(context.TODO(), "harvester-cluster-repo", metav1.GetOptions{})
	if err != nil {
		logrus.Errorf("Failed to get the harvester-cluster-repo deployment: %v", err)
		return ""
	}

	// ensure the harvester-cluster-repo deployment has only one container
	containerItem := deployContent.Spec.Template.Spec.Containers[0]
	if strings.HasPrefix(containerItem.Image, "rancher/harvester-cluster-repo") {
		return containerItem.Image
	}
	logrus.Errorf("Failed to get the harvester-cluster-repo Image: %v", containerItem.Image)
	return ""
}

func generateRWXVolPodName(pvc *corev1.PersistentVolumeClaim) string {
	return pvc.Spec.VolumeName
}

func (c *Controller) createExpanderPodIfNeed(pvc *corev1.PersistentVolumeClaim) error {
	expanderPodName := generateRWXVolPodName(pvc)
	_, err := c.coreClient.Pod().Get(pvc.Namespace, expanderPodName, metav1.GetOptions{})
	if err == nil {
		return nil
	}

	if !errors.IsNotFound(err) {
		return err
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: pvc.Namespace,
			Name:      expanderPodName,
		},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{
				{
					Name: pvc.Spec.VolumeName,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvc.Name,
						},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:            expanderName,
					Image:           c.getClusterRepoImage(),
					ImagePullPolicy: corev1.PullIfNotPresent,
					SecurityContext: &corev1.SecurityContext{
						Privileged: pointer.Bool(true),
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: pvc.Spec.VolumeName, MountPath: mntPathPrefix + pvc.Spec.VolumeName},
					},
					Command: []string{expanderCmd},
					Args:    []string{expanderArg},
				},
			},
		},
	}

	logrus.Infof("create pod %s/%s for expand netFS", pvc.Namespace, expanderPodName)
	if _, err := c.coreClient.Pod().Create(pod); err != nil {
		return err
	}

	return nil
}

func (c *Controller) doExpandNetworkFS(netFS *networkfsv1.NetworkFilesystem, pvc *corev1.PersistentVolumeClaim) (*networkfsv1.NetworkFilesystem, error) {
	if err := c.createExpanderPodIfNeed(pvc); err != nil {
		return nil, err
	}

	netFSCpy := netFS.DeepCopy()
	netFSCpy.Status.Status = networkfsv1.EndpointStatus(networkfsv1.ConditionTypeExpanding)
	if reflect.DeepEqual(netFS, netFSCpy) {
		return netFS, nil
	}
	return c.NetworkFilsystems.UpdateStatus(netFSCpy)
}

func (c *Controller) deleteExpanderPod(pvc *corev1.PersistentVolumeClaim) error {
	podName := generateRWXVolPodName(pvc)
	err := c.coreClient.Pod().Delete(pvc.Namespace, podName, &metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}

	if err != nil {
		return err
	}

	return nil
}

func (c *Controller) completeExpandNetworkFS(netFS *networkfsv1.NetworkFilesystem, pvc *corev1.PersistentVolumeClaim) (*networkfsv1.NetworkFilesystem, error) {
	logrus.Infof("Expand network filesystem %s completes or not needed", netFS.Name)

	if err := c.deleteExpanderPod(pvc); err != nil {
		return nil, err
	}

	netFSCpy := netFS.DeepCopy()
	netFSCpy.Status.Status = networkfsv1.EndpointStatus(networkfsv1.ConditionTypeReady)
	logrus.Infof("Update %s status as %s", netFSCpy.Name, netFSCpy.Status.Status)
	if reflect.DeepEqual(netFS, netFSCpy) {
		return netFS, nil
	}
	return c.NetworkFilsystems.UpdateStatus(netFSCpy)
}

func (c *Controller) expandNetworkFS(netFS *networkfsv1.NetworkFilesystem, pvc *corev1.PersistentVolumeClaim) (*networkfsv1.NetworkFilesystem, error) {
	logrus.Infof("Start expand network filesystem %s", netFS.Name)

	if netFS.Status.Status == networkfsv1.EndpointStatus(networkfsv1.ConditionTypeExpanding) {
		return netFS, nil
	}
	return c.doExpandNetworkFS(netFS, pvc)
}

func (c *Controller) getPVC(networkFS *networkfsv1.NetworkFilesystem) (*corev1.PersistentVolumeClaim, error) {
	lhVol, err := c.lhClient.LonghornV1beta2().Volumes(utils.LHNameSpace).Get(context.Background(), networkFS.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	pvcNameSpace := lhVol.Status.KubernetesStatus.Namespace
	pvcName := lhVol.Status.KubernetesStatus.PVCName
	return c.coreClient.PersistentVolumeClaim().Get(pvcNameSpace, pvcName, metav1.GetOptions{})
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
		networkFSCpy := networkFS.DeepCopy()
		networkFSCpy.Status.State = networkfsv1.NetworkFSStateEnabling
		networkFSCpy.Status.Status = networkfsv1.EndpointStatusNotReady
		networkFSCpy.Status.Type = networkfsv1.NetworkFSTypeNFS
		if !reflect.DeepEqual(networkFS, networkFSCpy) {
			return c.NetworkFilsystems.UpdateStatus(networkFSCpy)
		}
	}

	if lhShareMgr.Status.State != longhornv2.ShareManagerStateRunning {
		logrus.Infof("Wait the share manager %s to be running", networkFS.Name)
		return nil, fmt.Errorf("wait the share manager %s to be running", networkFS.Name)
	}

	// LH RWX volume endpoint should only have one address and one port
	service, err := c.coreClient.Service().Get(utils.LHNameSpace, networkFS.Name, metav1.GetOptions{})
	if err != nil {
		logrus.Errorf("Failed to get service %s: %v", networkFS.Name, err)
		return nil, err
	}
	networkFSCpy := networkFS.DeepCopy()
	if service.Spec.ClusterIP != corev1.ClusterIPNone {
		// means we depends on the service
		if service.Spec.ClusterIP == "" {
			// first init, service controller will update
			logrus.Infof("Skip update on networkfs controller with the first time service init")
			return nil, nil
		}
		networkFSCpy.Status.Endpoint = service.Spec.ClusterIP
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
		networkFSCpy.Status.Endpoint = endpoint.Subsets[0].Addresses[0].IP
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
	// update network filesystem status
	networkFSCpy.Status.State = networkfsv1.NetworkFSStateEnabled
	networkFSCpy.Status.Type = networkfsv1.NetworkFSTypeNFS
	networkFSCpy.Status.Status = networkfsv1.EndpointStatusReady
	networkFSCpy.Status.MountOpts = opts
	conds := networkfsv1.NetworkFSCondition{
		Type:               networkfsv1.ConditionTypeReady,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             "Endpoint is ready",
		Message:            "Endpoint contains the corresponding address",
	}
	networkFSCpy.Status.NetworkFSConds = utils.UpdateNetworkFSConds(networkFSCpy.Status.NetworkFSConds, conds)
	return c.NetworkFilsystems.UpdateStatus(networkFSCpy)
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

func isEnabled(networkFS *networkfsv1.NetworkFilesystem) bool {
	return networkFS.Status.State == networkfsv1.NetworkFSStateEnabled
}

func isDisabling(networkFS *networkfsv1.NetworkFilesystem) bool {
	return networkFS.Status.State == networkfsv1.NetworkFSStateDisabling
}

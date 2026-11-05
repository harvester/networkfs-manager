package status

import (
	"fmt"
	"reflect"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkfsv1 "github.com/harvester/networkfs-manager/pkg/apis/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/controller/endpoint"
	"github.com/harvester/networkfs-manager/pkg/utils"
)

type NetworkFSClient interface {
	Get(namespace, name string, options metav1.GetOptions) (*networkfsv1.NetworkFilesystem, error)
	UpdateStatus(networkFS *networkfsv1.NetworkFilesystem) (*networkfsv1.NetworkFilesystem, error)
}

// ReconcileEndpoint applies an endpoint selection to an enabled
// NetworkFilesystem and persists the status only when it changes.
func ReconcileEndpoint(client NetworkFSClient, namespace, volumeName string, selection endpoint.Selection) error {
	networkFS, err := client.Get(namespace, volumeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get networkFS %s: %w", volumeName, err)
	}
	if networkFS.Spec.DesiredState != networkfsv1.NetworkFSStateEnabled {
		logrus.Infof("Skip endpoint update because networkfilesystem %s is not enabled", networkFS.Name)
		return nil
	}

	networkFSCopy := applySelection(networkFS, selection)
	if reflect.DeepEqual(networkFS, networkFSCopy) {
		return nil
	}
	if _, err := client.UpdateStatus(networkFSCopy); err != nil {
		return fmt.Errorf("failed to update networkFS %s: %w", networkFS.Name, err)
	}
	return nil
}

// SetEndpointNotReady returns a copy of networkFS in the enabling state with
// an empty, not-ready NFS endpoint.
func SetEndpointNotReady(networkFS *networkfsv1.NetworkFilesystem, reason, message string) *networkfsv1.NetworkFilesystem {
	networkFSCopy := networkFS.DeepCopy()
	setEndpointStatus(networkFSCopy, "", networkfsv1.EndpointStatusNotReady)
	networkFSCopy.Status.NetworkFSConds = utils.UpdateNetworkFSConds(
		networkFSCopy.Status.NetworkFSConds,
		newCondition(networkfsv1.ConditionTypeNotReady, reason, message),
	)
	return networkFSCopy
}

// SetEndpointReady returns a copy of networkFS in the enabling state with the
// supplied ready NFS endpoint.
func SetEndpointReady(networkFS *networkfsv1.NetworkFilesystem, address, message string) *networkfsv1.NetworkFilesystem {
	networkFSCopy := networkFS.DeepCopy()
	setEndpointChanged(networkFSCopy, address)
	setEndpointStatus(networkFSCopy, address, networkfsv1.EndpointStatusReady)
	networkFSCopy.Status.NetworkFSConds = utils.UpdateNetworkFSConds(
		networkFSCopy.Status.NetworkFSConds,
		newCondition(networkfsv1.ConditionTypeReady, "Endpoint is ready", message),
	)
	return networkFSCopy
}

func applySelection(networkFS *networkfsv1.NetworkFilesystem, selection endpoint.Selection) *networkfsv1.NetworkFilesystem {
	notReadyReason := "Service is not ready"
	notReadyMessage := "Service did not contain the corresponding address"
	readyMessage := "Endpoint contains the corresponding address"
	if selection.Source == endpoint.SourceEndpointSlice {
		notReadyReason = "Endpoint is not ready"
		notReadyMessage = "EndpointSlice did not contain any ready address"
		readyMessage = "EndpointSlice contains the corresponding address"
	}

	if selection.Address == "" {
		return SetEndpointNotReady(networkFS, notReadyReason, notReadyMessage)
	}
	return SetEndpointReady(networkFS, selection.Address, readyMessage)
}

func setEndpointChanged(networkFS *networkfsv1.NetworkFilesystem, address string) {
	previous := networkFS.Status.Endpoint
	if previous == address {
		return
	}

	message := fmt.Sprintf("Endpoint address is initialized with %s", address)
	if previous != "" {
		message = fmt.Sprintf("Endpoint address is changed, previous address is %s", previous)
	}
	networkFS.Status.NetworkFSConds = utils.UpdateNetworkFSConds(
		networkFS.Status.NetworkFSConds,
		newCondition(networkfsv1.ConditionTypeEndpointChanged, "Endpoint is changed", message),
	)
}

func setEndpointStatus(networkFS *networkfsv1.NetworkFilesystem, address string, endpointStatus networkfsv1.EndpointStatus) {
	networkFS.Status.Endpoint = address
	networkFS.Status.Status = endpointStatus
	networkFS.Status.Type = networkfsv1.NetworkFSTypeNFS
	networkFS.Status.State = networkfsv1.NetworkFSStateEnabling
}

func newCondition(conditionType networkfsv1.ConditionType, reason, message string) networkfsv1.NetworkFSCondition {
	return networkfsv1.NetworkFSCondition{
		Type:               conditionType,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

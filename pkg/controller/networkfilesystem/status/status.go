package status

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkfsv1 "github.com/harvester/networkfs-manager/pkg/apis/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/controller/endpoint"
	"github.com/harvester/networkfs-manager/pkg/utils"
)

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

// ApplySelection changes endpoint status only when the address or readiness changes.
// Unchanged endpoints retain their state and condition timestamps.
func ApplySelection(networkFS *networkfsv1.NetworkFilesystem, selection endpoint.Selection) *networkfsv1.NetworkFilesystem {
	endpointStatus := networkfsv1.EndpointStatusReady
	if selection.Address == "" {
		endpointStatus = networkfsv1.EndpointStatusNotReady
	}
	if networkFS.Status.Endpoint == selection.Address && networkFS.Status.Status == endpointStatus {
		return networkFS
	}

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

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

	networkFSCopy := fromSelection(networkFS, selection)
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
	if networkFSCopy.Status.Endpoint != address {
		changedMessage := fmt.Sprintf("Endpoint address is initialized with %s", address)
		if networkFSCopy.Status.Endpoint != "" {
			changedMessage = fmt.Sprintf("Endpoint address is changed, previous address is %s", networkFSCopy.Status.Endpoint)
		}
		networkFSCopy.Status.NetworkFSConds = utils.UpdateNetworkFSConds(
			networkFSCopy.Status.NetworkFSConds,
			newCondition(networkfsv1.ConditionTypeEndpointChanged, "Endpoint is changed", changedMessage),
		)
	}

	setEndpointStatus(networkFSCopy, address, networkfsv1.EndpointStatusReady)
	networkFSCopy.Status.NetworkFSConds = utils.UpdateNetworkFSConds(
		networkFSCopy.Status.NetworkFSConds,
		newCondition(networkfsv1.ConditionTypeReady, "Endpoint is ready", message),
	)
	return networkFSCopy
}

func fromSelection(networkFS *networkfsv1.NetworkFilesystem, selection endpoint.Selection) *networkfsv1.NetworkFilesystem {
	if selection.Address == "" {
		if selection.Source == endpoint.SourceEndpointSlice {
			return SetEndpointNotReady(
				networkFS,
				"Endpoint is not ready",
				"EndpointSlice did not contain any ready address",
			)
		}
		return SetEndpointNotReady(
			networkFS,
			"Service is not ready",
			"Service did not contain the corresponding address",
		)
	}

	if selection.Source == endpoint.SourceEndpointSlice {
		return SetEndpointReady(
			networkFS,
			selection.Address,
			"EndpointSlice contains the corresponding address",
		)
	}
	return SetEndpointReady(
		networkFS,
		selection.Address,
		"Endpoint contains the corresponding address",
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

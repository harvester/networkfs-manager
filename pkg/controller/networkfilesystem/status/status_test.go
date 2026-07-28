package status

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkfsv1 "github.com/harvester/networkfs-manager/pkg/apis/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/controller/endpoint"
)

type fakeNetworkFSClient struct {
	networkFS   *networkfsv1.NetworkFilesystem
	updated     *networkfsv1.NetworkFilesystem
	getErr      error
	updateErr   error
	getCalls    int
	updateCalls int
}

func (f *fakeNetworkFSClient) Get(_, _ string, _ metav1.GetOptions) (*networkfsv1.NetworkFilesystem, error) {
	f.getCalls++
	return f.networkFS, f.getErr
}

func (f *fakeNetworkFSClient) UpdateStatus(networkFS *networkfsv1.NetworkFilesystem) (*networkfsv1.NetworkFilesystem, error) {
	f.updateCalls++
	f.updated = networkFS
	return networkFS, f.updateErr
}

func TestReconcileEndpoint(t *testing.T) {
	t.Run("applies Service endpoint", func(t *testing.T) {
		client := &fakeNetworkFSClient{
			networkFS: enabledNetworkFS("pvc-test"),
		}
		selection := endpoint.Selection{
			Address: "182.16.0.6",
			Source:  endpoint.SourceLoadBalancerIP,
		}

		if err := ReconcileEndpoint(client, "default", "pvc-test", selection); err != nil {
			t.Fatal(err)
		}
		if client.updateCalls != 1 {
			t.Fatalf("ReconcileEndpoint() made %d updates, want 1", client.updateCalls)
		}
		if client.updated.Status.Endpoint != selection.Address {
			t.Fatalf("updated endpoint = %q, want %q", client.updated.Status.Endpoint, selection.Address)
		}
		assertCondition(
			t,
			client.updated,
			networkfsv1.ConditionTypeReady,
			"Endpoint is ready",
			"Endpoint contains the corresponding address",
		)
	})

	t.Run("applies not-ready EndpointSlice", func(t *testing.T) {
		client := &fakeNetworkFSClient{
			networkFS: enabledNetworkFS("pvc-test"),
		}
		selection := endpoint.Selection{Source: endpoint.SourceEndpointSlice}

		if err := ReconcileEndpoint(client, "default", "pvc-test", selection); err != nil {
			t.Fatal(err)
		}
		if client.updated.Status.Status != networkfsv1.EndpointStatusNotReady {
			t.Fatalf("updated endpoint status = %q, want %q",
				client.updated.Status.Status, networkfsv1.EndpointStatusNotReady)
		}
		assertCondition(
			t,
			client.updated,
			networkfsv1.ConditionTypeNotReady,
			"Endpoint is not ready",
			"EndpointSlice did not contain any ready address",
		)
	})

	t.Run("skips disabled NetworkFilesystem", func(t *testing.T) {
		networkFS := enabledNetworkFS("pvc-test")
		networkFS.Spec.DesiredState = networkfsv1.NetworkFSStateDisabled
		client := &fakeNetworkFSClient{networkFS: networkFS}

		if err := ReconcileEndpoint(client, "default", "pvc-test", endpoint.Selection{}); err != nil {
			t.Fatal(err)
		}
		if client.updateCalls != 0 {
			t.Fatalf("ReconcileEndpoint() made %d updates, want 0", client.updateCalls)
		}
	})

	t.Run("returns client errors", func(t *testing.T) {
		getErr := errors.New("get failed")
		if err := ReconcileEndpoint(
			&fakeNetworkFSClient{getErr: getErr},
			"default",
			"pvc-test",
			endpoint.Selection{},
		); !errors.Is(err, getErr) {
			t.Fatalf("ReconcileEndpoint() error = %v, want %v", err, getErr)
		}

		updateErr := errors.New("update failed")
		if err := ReconcileEndpoint(
			&fakeNetworkFSClient{
				networkFS: enabledNetworkFS("pvc-test"),
				updateErr: updateErr,
			},
			"default",
			"pvc-test",
			endpoint.Selection{Address: "10.0.0.8", Source: endpoint.SourceClusterIP},
		); !errors.Is(err, updateErr) {
			t.Fatalf("ReconcileEndpoint() error = %v, want %v", err, updateErr)
		}
	})
}

func enabledNetworkFS(name string) *networkfsv1.NetworkFilesystem {
	return &networkfsv1.NetworkFilesystem{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: networkfsv1.NetworkFSSpec{
			DesiredState: networkfsv1.NetworkFSStateEnabled,
		},
	}
}

func TestSetEndpointNotReady(t *testing.T) {
	networkFS := &networkfsv1.NetworkFilesystem{
		Status: networkfsv1.NetworkFSStatus{
			Endpoint: "10.0.0.8",
			State:    networkfsv1.NetworkFSStateEnabled,
			Status:   networkfsv1.EndpointStatusReady,
		},
	}

	got := SetEndpointNotReady(networkFS, "Service is not ready", "address is pending")

	if networkFS.Status.Endpoint != "10.0.0.8" {
		t.Fatal("SetEndpointNotReady() modified the input")
	}
	if got.Status.Endpoint != "" ||
		got.Status.State != networkfsv1.NetworkFSStateEnabling ||
		got.Status.Status != networkfsv1.EndpointStatusNotReady ||
		got.Status.Type != networkfsv1.NetworkFSTypeNFS {
		t.Fatalf("SetEndpointNotReady() status = %#v", got.Status)
	}
	assertCondition(t, got, networkfsv1.ConditionTypeNotReady, "Service is not ready", "address is pending")
}

func TestSetEndpointReady(t *testing.T) {
	tests := []struct {
		name               string
		previousAddress    string
		wantChangedMessage string
		wantChanged        bool
	}{
		{
			name:               "initializes endpoint",
			wantChanged:        true,
			wantChangedMessage: "Endpoint address is initialized with 10.0.0.8",
		},
		{
			name:               "changes endpoint",
			previousAddress:    "10.0.0.7",
			wantChanged:        true,
			wantChangedMessage: "Endpoint address is changed, previous address is 10.0.0.7",
		},
		{
			name:            "keeps unchanged endpoint",
			previousAddress: "10.0.0.8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			networkFS := &networkfsv1.NetworkFilesystem{
				Status: networkfsv1.NetworkFSStatus{Endpoint: tt.previousAddress},
			}

			got := SetEndpointReady(networkFS, "10.0.0.8", "endpoint contains an address")

			if networkFS.Status.Endpoint != tt.previousAddress {
				t.Fatal("SetEndpointReady() modified the input")
			}
			if got.Status.Endpoint != "10.0.0.8" ||
				got.Status.State != networkfsv1.NetworkFSStateEnabling ||
				got.Status.Status != networkfsv1.EndpointStatusReady ||
				got.Status.Type != networkfsv1.NetworkFSTypeNFS {
				t.Fatalf("SetEndpointReady() status = %#v", got.Status)
			}
			assertCondition(t, got, networkfsv1.ConditionTypeReady, "Endpoint is ready", "endpoint contains an address")

			condition, found := findCondition(got, networkfsv1.ConditionTypeEndpointChanged)
			if found != tt.wantChanged {
				t.Fatalf("EndpointChanged condition found = %v, want %v", found, tt.wantChanged)
			}
			if found && condition.Message != tt.wantChangedMessage {
				t.Fatalf("EndpointChanged message = %q, want %q", condition.Message, tt.wantChangedMessage)
			}
		})
	}
}

func assertCondition(t *testing.T, networkFS *networkfsv1.NetworkFilesystem, conditionType networkfsv1.ConditionType, reason, message string) {
	t.Helper()
	condition, found := findCondition(networkFS, conditionType)
	if !found {
		t.Fatalf("condition %s was not found", conditionType)
	}
	if condition.Status != corev1.ConditionTrue ||
		condition.Reason != reason ||
		condition.Message != message ||
		condition.LastTransitionTime.IsZero() {
		t.Fatalf("condition %s = %#v", conditionType, condition)
	}
}

func findCondition(networkFS *networkfsv1.NetworkFilesystem, conditionType networkfsv1.ConditionType) (networkfsv1.NetworkFSCondition, bool) {
	for _, condition := range networkFS.Status.NetworkFSConds {
		if condition.Type == conditionType {
			return condition, true
		}
	}
	return networkfsv1.NetworkFSCondition{}, false
}

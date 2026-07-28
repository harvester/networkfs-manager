package status

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkfsv1 "github.com/harvester/networkfs-manager/pkg/apis/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/controller/endpoint"
)

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

func TestApplySelection(t *testing.T) {
	for _, source := range []endpoint.Source{endpoint.SourceLoadBalancerIP, endpoint.SourceEndpointSlice} {
		for _, address := range []string{"", "192.168.100.224"} {
			t.Run(string(source)+"/"+address, func(t *testing.T) {
				networkFS := &networkfsv1.NetworkFilesystem{
					ObjectMeta: metav1.ObjectMeta{Name: "pvc-test"},
					Status: networkfsv1.NetworkFSStatus{
						Endpoint: "192.168.100.3", State: networkfsv1.NetworkFSStateEnabled,
						Status: networkfsv1.EndpointStatusReady,
					},
				}
				selection := endpoint.Selection{Source: source, Address: address}
				got := ApplySelection(networkFS, selection)
				wantStatus := networkfsv1.EndpointStatusReady
				if address == "" {
					wantStatus = networkfsv1.EndpointStatusNotReady
				}
				if got.Status.Endpoint != address || got.Status.Status != wantStatus ||
					got.Status.State != networkfsv1.NetworkFSStateEnabling {
					t.Fatalf("unexpected endpoint status: %+v", got.Status)
				}
				if networkFS.Status.Endpoint != "192.168.100.3" {
					t.Fatal("modified the cached NetworkFilesystem")
				}
				if again := ApplySelection(got, selection); again != got {
					t.Fatal("unchanged selection rewrote status or condition timestamps")
				}
				got.Status.State = networkfsv1.NetworkFSStateEnabled
				if again := ApplySelection(got, selection); again != got {
					t.Fatal("unchanged selection restarted the enable flow")
				}
			})
		}
	}
}

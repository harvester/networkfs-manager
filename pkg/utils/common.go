package utils

import (
	"fmt"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	networkfsv1 "github.com/harvester/networkfs-manager/pkg/apis/harvesterhci.io/v1beta1"
)

type Option struct {
	KubeConfig  string
	Namespace   string
	NodeName    string
	Debug       bool
	Threadiness int
}

// These values are set via linker flags in scripts/build
var (
	Version     = "v0.0.0-dev"
	GitCommit   = "HEAD"
	LHNameSpace = "longhorn-system"
)

func FriendlyVersion() string {
	return fmt.Sprintf("%s (%s)", Version, GitCommit)
}

func UpdateNetworkFSConds(curConds []networkfsv1.NetworkFSCondition, c networkfsv1.NetworkFSCondition) []networkfsv1.NetworkFSCondition {
	found := false
	var pod = 0
	logrus.Infof("Prepare to check the coming Type: %s, Status: %s", c.Type, c.Status)
	for id, cond := range curConds {
		if cond.Type == c.Type {
			found = true
			pod = id
			break
		}
	}

	if found {
		curConds[pod] = c
	} else {
		curConds = append(curConds, c)
	}
	return curConds

}

func PVCNeedExpand(pvc *corev1.PersistentVolumeClaim) bool {
	return pvc.Spec.Resources.Requests.Storage().Value() != pvc.Status.Capacity.Storage().Value()
}

func NetFSInExpanding(netFS *networkfsv1.NetworkFilesystem) bool {
	return netFS.Status.Status == networkfsv1.EndpointStatus(networkfsv1.ConditionTypeExpanding)
}

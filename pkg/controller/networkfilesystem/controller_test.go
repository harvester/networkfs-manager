package networkfilesystem

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	longhornv2 "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
	lhclientset "github.com/longhorn/longhorn-manager/k8s/pkg/client/clientset/versioned"
	ctlcorev1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	ctldiscoveryv1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/discovery/v1"
	"github.com/rancher/wrangler/v3/pkg/generic"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	corelisters "k8s.io/client-go/listers/core/v1"
	discoverylisters "k8s.io/client-go/listers/discovery/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	networkfsv1 "github.com/harvester/networkfs-manager/pkg/apis/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/controller/endpoint"
	"github.com/harvester/networkfs-manager/pkg/controller/endpointslice"
	"github.com/harvester/networkfs-manager/pkg/controller/service"
	ctlnfs "github.com/harvester/networkfs-manager/pkg/generated/controllers/harvesterhci.io/v1beta1"
	"github.com/harvester/networkfs-manager/pkg/utils"
)

const (
	testVolume = "pvc-test"
	testNS     = "harvester-system"
	testVIP    = "192.168.100.224"
	testPodIP  = "192.168.100.3"
)

type serviceCache struct {
	ctlcorev1.ServiceCache
	lister corelisters.ServiceLister
}

func (f *serviceCache) Get(namespace, name string) (*corev1.Service, error) {
	return f.lister.Services(namespace).Get(name)
}

func (f *serviceCache) List(namespace string, selector labels.Selector) ([]*corev1.Service, error) {
	return f.lister.Services(namespace).List(selector)
}

type sliceCache struct {
	ctldiscoveryv1.EndpointSliceCache
	lister discoverylisters.EndpointSliceLister
}

func (f *sliceCache) List(namespace string, selector labels.Selector) ([]*discoveryv1.EndpointSlice, error) {
	return f.lister.EndpointSlices(namespace).List(selector)
}

type fakeNetworkFS struct {
	ctlnfs.NetworkFilesystemController
	object  *networkfsv1.NetworkFilesystem
	queue   workqueue.TypedInterface[string]
	writes  []string
	enqueue func()
}

func (f *fakeNetworkFS) Enqueue(namespace, name string) {
	if f.enqueue != nil {
		f.enqueue()
	}
	f.queue.Add(namespace + "/" + name)
}

func (f *fakeNetworkFS) UpdateStatus(obj *networkfsv1.NetworkFilesystem) (*networkfsv1.NetworkFilesystem, error) {
	if obj.ResourceVersion != f.object.ResourceVersion {
		return nil, apierrors.NewConflict(
			schema.GroupResource{Resource: "networkfilesystems"}, obj.Name, fmt.Errorf("stale resourceVersion"),
		)
	}
	rv, err := strconv.Atoi(obj.ResourceVersion)
	if err != nil {
		return nil, err
	}
	f.object = obj.DeepCopy()
	f.object.ResourceVersion = strconv.Itoa(rv + 1)
	f.writes = append(f.writes, obj.Status.Endpoint)
	// Status updates also generate events on the NetworkFilesystem queue.
	f.queue.Add(obj.Namespace + "/" + obj.Name)
	return f.object.DeepCopy(), nil
}

func newController(t *testing.T) (*Controller, *fakeNetworkFS, cache.Indexer, cache.Indexer) {
	t.Helper()
	indexes := cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}
	services := cache.NewIndexer(cache.MetaNamespaceKeyFunc, indexes)
	slices := cache.NewIndexer(cache.MetaNamespaceKeyFunc, indexes)
	nfs := &fakeNetworkFS{
		queue: workqueue.NewTyped[string](),
		object: &networkfsv1.NetworkFilesystem{
			ObjectMeta: metav1.ObjectMeta{Name: testVolume, Namespace: testNS, ResourceVersion: "1"},
			Spec:       networkfsv1.NetworkFSSpec{DesiredState: networkfsv1.NetworkFSStateEnabled},
			Status: networkfsv1.NetworkFSStatus{
				Endpoint: "192.168.100.2", State: networkfsv1.NetworkFSStateEnabled,
				Status: networkfsv1.EndpointStatusReady,
			},
		},
	}
	t.Cleanup(nfs.queue.ShutDown)
	c := &Controller{
		serviceCache:       &serviceCache{lister: corelisters.NewServiceLister(services)},
		endpointSliceCache: &sliceCache{lister: discoverylisters.NewEndpointSliceLister(slices)},
		NetworkFilsystems:  nfs,
	}
	return c, nfs, services, slices
}

func legacyService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: testVolume, Namespace: utils.LHNameSpace},
		Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone},
	}
}

func vipService(address string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rwx-service", Namespace: utils.LHNameSpace,
			Labels: map[string]string{endpoint.RWXVolumeServiceLabel: testVolume},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, LoadBalancerIP: address},
	}
}

func legacySlice() *discoveryv1.EndpointSlice {
	nfsPort := "nfs"
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: testVolume + "-generated", Namespace: utils.LHNameSpace,
			Labels: map[string]string{discoveryv1.LabelServiceName: testVolume},
		},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{testPodIP}}},
		Ports:     []discoveryv1.EndpointPort{{Name: &nfsPort}},
	}
}

func addObject(t *testing.T, indexer cache.Indexer, obj interface{}) {
	t.Helper()
	if err := indexer.Add(obj); err != nil {
		t.Fatal(err)
	}
}

func TestOnNetworkFSChange(t *testing.T) {
	tests := []struct {
		name     string
		service  *corev1.Service
		previous string
		want     string
	}{
		{name: "VIP replaces Pod IP", service: vipService(testVIP), want: testVIP},
		{name: "pending VIP blocks fallback", service: vipService(""), want: ""},
		{name: "legacy headless service", want: testPodIP},
		{name: "unchanged VIP", service: vipService(testVIP), previous: testVIP, want: testVIP},
		{
			name: "legacy ClusterIP", want: "10.43.0.8",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: testVolume, Namespace: utils.LHNameSpace},
				Spec:       corev1.ServiceSpec{ClusterIP: "10.43.0.8"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, nfs, services, slices := newController(t)
			addObject(t, services, legacyService())
			addObject(t, slices, legacySlice())
			if tt.service != nil {
				addObject(t, services, tt.service)
			}
			if tt.previous != "" {
				nfs.object.Status.Endpoint = tt.previous
			}
			input := nfs.object.DeepCopy()
			before := input.DeepCopy()
			if _, err := c.OnNetworkFSChange("", input); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(input, before) {
				t.Fatal("modified the cached NetworkFilesystem")
			}
			if nfs.object.Status.Endpoint != tt.want {
				t.Fatalf("endpoint = %q, want %q", nfs.object.Status.Endpoint, tt.want)
			}
			if before.Status.Endpoint == tt.want && len(nfs.writes) != 0 {
				t.Fatal("unchanged endpoint caused a status write")
			}
			if tt.want == "" && nfs.object.Status.Status != networkfsv1.EndpointStatusNotReady {
				t.Fatal("pending VIP is not marked not-ready")
			}
		})
	}
}

func TestEndpointConflict(t *testing.T) {
	c, nfs, services, slices := newController(t)
	addObject(t, services, legacyService())
	addObject(t, slices, legacySlice())
	stale := nfs.object.DeepCopy()
	nfs.object.ResourceVersion = "2"
	if _, err := c.OnNetworkFSChange("", stale); !apierrors.IsConflict(err) {
		t.Fatalf("error = %v, want conflict", err)
	}
	addObject(t, services, vipService(testVIP))
	if _, err := c.OnNetworkFSChange("", nfs.object.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(nfs.writes, []string{testVIP}) {
		t.Fatalf("retry published stale selection: %v", nfs.writes)
	}
}

func TestDisabledVolume(t *testing.T) {
	c, nfs, _, _ := newController(t)
	nfs.object.Spec.DesiredState = networkfsv1.NetworkFSStateDisabled
	nfs.object.Status.State = networkfsv1.NetworkFSStateDisabled
	if _, err := c.OnNetworkFSChange("", nfs.object.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	if len(nfs.writes) != 0 {
		t.Fatal("disabled volume received an endpoint update")
	}
}

func TestMissingService(t *testing.T) {
	c, nfs, _, _ := newController(t)
	if _, err := c.OnNetworkFSChange("", nfs.object.DeepCopy()); !apierrors.IsNotFound(err) {
		t.Fatalf("error = %v, want NotFound so reconciliation retries", err)
	}
	if len(nfs.writes) != 0 {
		t.Fatal("missing service caused a status write")
	}
}

func TestPendingVIP(t *testing.T) {
	c, nfs, services, slices := newController(t)
	configureEnable(t, c)
	addObject(t, services, legacyService())
	addObject(t, slices, legacySlice())
	addObject(t, services, vipService(""))
	nfs.Enqueue(testNS, testVolume)
	drainQueue(t, c, nfs)
	if nfs.object.Status.Endpoint != "" || nfs.object.Status.State != networkfsv1.NetworkFSStateEnabling {
		t.Fatalf("pending VIP did not clear the old endpoint: %+v", nfs.object.Status)
	}
	addObject(t, services, vipService(testVIP))
	nfs.Enqueue(testNS, testVolume)
	drainQueue(t, c, nfs)
	if nfs.object.Status.Endpoint != testVIP || nfs.object.Status.State != networkfsv1.NetworkFSStateEnabled {
		t.Fatalf("volume did not enable with the assigned VIP: %+v", nfs.object.Status)
	}
}

type serviceEvents struct {
	ctlcorev1.ServiceController
	cache   ctlcorev1.ServiceCache
	handler generic.ObjectHandler[*corev1.Service]
}

func (f *serviceEvents) Cache() generic.CacheInterface[*corev1.Service] {
	return f.cache
}

func (f *serviceEvents) OnChange(_ context.Context, _ string, handler generic.ObjectHandler[*corev1.Service]) {
	f.handler = handler
}

type sliceEvents struct {
	ctldiscoveryv1.EndpointSliceController
	handler generic.ObjectHandler[*discoveryv1.EndpointSlice]
}

func (f *sliceEvents) OnChange(_ context.Context, _ string, handler generic.ObjectHandler[*discoveryv1.EndpointSlice]) {
	f.handler = handler
}

func TestDelayedSliceKeepsVIP(t *testing.T) {
	c, nfs, services, slices := newController(t)
	addObject(t, services, legacyService())
	addObject(t, slices, legacySlice())
	configureEnable(t, c)
	serviceEvents := &serviceEvents{cache: c.serviceCache}
	sliceEvents := &sliceEvents{}
	opt := &utils.Option{Namespace: testNS}
	if err := service.Register(context.Background(), serviceEvents, nfs, opt); err != nil {
		t.Fatal(err)
	}
	if err := endpointslice.Register(context.Background(), sliceEvents, nfs, serviceEvents, opt); err != nil {
		t.Fatal(err)
	}

	// Hold the old slice event after volume lookup, before it queues reconciliation.
	// A newer Service event must be able to publish the VIP before this event resumes.
	blocked, resume := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var release sync.Once
	unblock := func() { release.Do(func() { close(resume) }) }
	t.Cleanup(unblock)
	nfs.enqueue = func() {
		if calls.Add(1) == 1 {
			close(blocked)
			<-resume
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := sliceEvents.handler("", legacySlice())
		done <- err
	}()
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("slice handler did not reach enqueue")
	}
	preferred := vipService(testVIP)
	addObject(t, services, preferred)
	if _, err := serviceEvents.handler("", preferred); err != nil {
		t.Fatal(err)
	}
	drainQueue(t, c, nfs)
	if nfs.object.Status.Endpoint != testVIP || len(nfs.writes) == 0 {
		t.Fatalf("Service event did not publish the VIP: %+v", nfs.object.Status)
	}
	writes := len(nfs.writes)
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slice handler did not finish")
	}
	drainQueue(t, c, nfs)
	if nfs.object.Status.Endpoint != testVIP || len(nfs.writes) != writes {
		t.Fatalf("delayed slice changed the VIP; writes = %v", nfs.writes)
	}
	if nfs.object.Status.State != networkfsv1.NetworkFSStateEnabled {
		t.Fatalf("volume did not settle in enabled state: %+v", nfs.object.Status)
	}
}

func drainQueue(t *testing.T, c *Controller, nfs *fakeNetworkFS) {
	t.Helper()
	for attempts := 0; nfs.queue.Len() > 0; attempts++ {
		if attempts == 10 {
			t.Fatal("reconciliation did not settle")
		}
		key, _ := nfs.queue.Get()
		if key != testNS+"/"+testVolume {
			t.Fatalf("unexpected queue key: %s", key)
		}
		_, err := c.OnNetworkFSChange(key, nfs.object.DeepCopy())
		nfs.queue.Done(key)
		if err != nil {
			t.Fatal(err)
		}
	}
}

type fakeCore struct {
	ctlcorev1.Interface
}

func (f *fakeCore) PersistentVolume() ctlcorev1.PersistentVolumeController {
	return &fakePV{}
}

type fakePV struct {
	ctlcorev1.PersistentVolumeController
}

func (f *fakePV) Get(_ string, _ metav1.GetOptions) (*corev1.PersistentVolume, error) {
	return &corev1.PersistentVolume{Spec: corev1.PersistentVolumeSpec{
		PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{}},
	}}, nil
}

func configureEnable(t *testing.T, c *Controller) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := "/apis/longhorn.io/v1beta2/namespaces/longhorn-system/sharemanagers/" + testVolume
		if r.Method != http.MethodGet || r.URL.Path != path {
			http.Error(w, "unexpected Longhorn request", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&longhornv2.ShareManager{
			TypeMeta: metav1.TypeMeta{APIVersion: "longhorn.io/v1beta2", Kind: "ShareManager"},
			Status:   longhornv2.ShareManagerStatus{State: longhornv2.ShareManagerStateRunning},
		})
	}))
	t.Cleanup(server.Close)
	client, err := lhclientset.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	c.lhClient = client
	c.coreClient = &fakeCore{}
}

package wsgateway

import (
	"context"
	"sort"
	"testing"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func boolPtr(b bool) *bool    { return &b }
func int32Ptr(i int32) *int32 { return &i }

// newTestEndpointSlice builds a race-service EndpointSlice carrying the
// same kubernetes.io/service-name label the real EndpointSlice
// controller always sets — this is what recompute's own label selector
// actually filters on, not something this test can skip.
func newTestEndpointSlice(name, namespace string, port int32, endpoints ...discoveryv1.Endpoint) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"kubernetes.io/service-name": "race-service"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   endpoints,
		Ports:       []discoveryv1.EndpointPort{{Port: int32Ptr(port)}},
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func TestK8sBackendDiscovery_InitialSync_OnlyReadyEndpoints(t *testing.T) {
	slice := newTestEndpointSlice("race-service-abc", "aviron", 8080,
		discoveryv1.Endpoint{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)}},
		discoveryv1.Endpoint{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(false)}},
		// Nil Ready must be treated as ready — discoveryv1.EndpointConditions'
		// own doc: "A nil value should be interpreted as true".
		discoveryv1.Endpoint{Addresses: []string{"10.0.0.3"}, Conditions: discoveryv1.EndpointConditions{}},
	)
	clientset := fake.NewSimpleClientset(slice)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d, err := NewK8sBackendDiscovery(ctx, clientset, "aviron", testLogger)
	if err != nil {
		t.Fatalf("NewK8sBackendDiscovery: %v", err)
	}
	if !d.WaitForSync(ctx) {
		t.Fatal("WaitForSync returned false")
	}

	// recompute runs asynchronously off the informer's AddFunc — give it a
	// moment past WaitForSync (which only guarantees the store is
	// populated, not that this package's own event handler already ran).
	deadline := time.Now().Add(2 * time.Second)
	var got []string
	for time.Now().Before(deadline) {
		got = d.Backends()
		if len(got) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	want := []string{"10.0.0.1:8080", "10.0.0.3:8080"}
	if len(got) != 2 {
		t.Fatalf("Backends() = %v, want 2 ready entries (got %d)", got, len(got))
	}
	gotSorted := sorted(got)
	for i := range want {
		if gotSorted[i] != want[i] {
			t.Errorf("Backends()[%d] = %q, want %q (full: %v)", i, gotSorted[i], want[i], gotSorted)
		}
	}
}

func TestK8sBackendDiscovery_NoMatchingSlices_EmptyNotNil(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d, err := NewK8sBackendDiscovery(ctx, clientset, "aviron", testLogger)
	if err != nil {
		t.Fatalf("NewK8sBackendDiscovery: %v", err)
	}
	if !d.WaitForSync(ctx) {
		t.Fatal("WaitForSync returned false")
	}

	if got := d.Backends(); len(got) != 0 {
		t.Fatalf("Backends() = %v, want empty", got)
	}
}

// TestK8sBackendDiscovery_ReactsToLiveCreate proves the informer's own
// watch (not just the initial List) drives Backends() — a slice created
// after the informer has already synced must still show up, without a
// second WaitForSync call.
func TestK8sBackendDiscovery_ReactsToLiveCreate(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d, err := NewK8sBackendDiscovery(ctx, clientset, "aviron", testLogger)
	if err != nil {
		t.Fatalf("NewK8sBackendDiscovery: %v", err)
	}
	if !d.WaitForSync(ctx) {
		t.Fatal("WaitForSync returned false")
	}
	if got := d.Backends(); len(got) != 0 {
		t.Fatalf("Backends() before create = %v, want empty", got)
	}

	slice := newTestEndpointSlice("race-service-new", "aviron", 8080,
		discoveryv1.Endpoint{Addresses: []string{"10.0.0.9"}, Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)}},
	)
	if _, err := clientset.DiscoveryV1().EndpointSlices("aviron").Create(ctx, slice, metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := d.Backends(); len(got) == 1 && got[0] == "10.0.0.9:8080" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Backends() never reflected the live-created slice, got %v", d.Backends())
}

// TestK8sBackendDiscovery_HasSynced_TrueAfterWaitForSync proves the
// non-blocking HasSynced (healthz.go's syncedChecker) agrees with the
// blocking WaitForSync it mirrors, once sync has actually completed.
func TestK8sBackendDiscovery_HasSynced_TrueAfterWaitForSync(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d, err := NewK8sBackendDiscovery(ctx, clientset, "aviron", testLogger)
	if err != nil {
		t.Fatalf("NewK8sBackendDiscovery: %v", err)
	}

	if !d.WaitForSync(ctx) {
		t.Fatal("WaitForSync returned false")
	}
	if !d.HasSynced() {
		t.Fatal("HasSynced() = false after WaitForSync returned true")
	}
}

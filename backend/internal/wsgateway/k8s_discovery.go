package wsgateway

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// raceServiceLabelSelector scopes the informer to exactly the
// EndpointSlice objects k8s-core-infra.md's race-service headless
// Service owns — every EndpointSlice Kubernetes creates for a Service
// carries this label automatically, no manual tagging needed on our
// side. informerResync is the periodic full relist client-go's own
// SharedIndexInformer already performs as a self-healing fallback on
// top of its watch — unrelated to how quickly a real add/remove is
// observed, which is push-based via the event handlers below.
const (
	raceServiceLabelSelector = "kubernetes.io/service-name=race-service"
	informerResync           = 30 * time.Second
)

// K8sBackendDiscovery watches EndpointSlice objects for the race-service
// Service and exposes its current Ready-endpoint pool as host:port
// strings (dynamic-backend-discovery.md) — the dynamic counterpart to
// StaticBackends, used once ws-gateway is actually running in-cluster.
// Backends() is a single atomic load, the same cost profile the old
// fixed []string field had, safe to call on every room-less request.
type K8sBackendDiscovery struct {
	informer cache.SharedIndexInformer
	backends atomic.Pointer[[]string]
	logger   *slog.Logger
}

// NewK8sBackendDiscovery constructs a K8sBackendDiscovery and starts its
// informer against ctx — the caller (cmd/ws-gateway/run.go) passes the
// process's own root context, so the informer's background goroutine
// shuts down alongside every other one graceful-shutdown.md already
// coordinates, not a separately-managed lifecycle.
func NewK8sBackendDiscovery(ctx context.Context, clientset kubernetes.Interface, namespace string, logger *slog.Logger) (*K8sBackendDiscovery, error) {
	factory := informers.NewSharedInformerFactoryWithOptions(
		clientset, informerResync,
		informers.WithNamespace(namespace),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.LabelSelector = raceServiceLabelSelector
		}),
	)
	informer := factory.Discovery().V1().EndpointSlices().Informer()

	d := &K8sBackendDiscovery{informer: informer, logger: logger}
	empty := []string{}
	d.backends.Store(&empty)

	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { d.recompute() },
		UpdateFunc: func(any, any) { d.recompute() },
		DeleteFunc: func(any) { d.recompute() },
	})
	if err != nil {
		return nil, fmt.Errorf("wsgateway: register endpointslice handler: %w", err)
	}

	factory.Start(ctx.Done())
	return d, nil
}

// Backends satisfies BackendDiscovery.
func (d *K8sBackendDiscovery) Backends() []string {
	return *d.backends.Load()
}

// WaitForSync blocks until the informer's initial List has completed, or
// ctx is done — used once, at startup, before cmd/ws-gateway/run.go
// starts serving traffic at all.
func (d *K8sBackendDiscovery) WaitForSync(ctx context.Context) bool {
	return cache.WaitForCacheSync(ctx.Done(), d.informer.HasSynced)
}

// HasSynced satisfies healthz.go's syncedChecker — a non-blocking read,
// unlike WaitForSync, safe to call on every GET /healthz request so a
// pod that passed WaitForSync once at startup but somehow lost sync
// later still reports it accurately.
func (d *K8sBackendDiscovery) HasSynced() bool {
	return d.informer.HasSynced()
}

// recompute rebuilds the backend list from every EndpointSlice currently
// in the informer's local store, keeping only Ready endpoints — a pod
// still starting, or mid-terminationGracePeriodSeconds graceful
// shutdown, must not receive fresh room-less traffic. This respects the
// exact readiness signal graceful-shutdown.md's ReadinessGate already
// produces (kubelet only marks an EndpointSlice entry Ready once
// /healthz passes), not a second liveness check invented here.
func (d *K8sBackendDiscovery) recompute() {
	var backends []string
	for _, obj := range d.informer.GetStore().List() {
		slice, ok := obj.(*discoveryv1.EndpointSlice)
		if !ok {
			continue
		}
		if len(slice.Ports) == 0 || slice.Ports[0].Port == nil {
			continue
		}
		port := *slice.Ports[0].Port
		for _, ep := range slice.Endpoints {
			// discoveryv1.EndpointConditions.Ready's own doc: "A nil value
			// should be interpreted as true" — only an explicit false
			// (a pod actually failing its readiness probe) excludes it.
			if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
				continue
			}
			if len(ep.Addresses) == 0 {
				continue
			}
			backends = append(backends, fmt.Sprintf("%s:%d", ep.Addresses[0], port))
		}
	}
	d.backends.Store(&backends)
	d.logger.Info("wsgateway: race-service backend pool updated", slog.Int("count", len(backends)))
}

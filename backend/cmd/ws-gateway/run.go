package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/akkien/aviron/internal/redisclient"
	"github.com/akkien/aviron/internal/roomlocator"
	"github.com/akkien/aviron/internal/roomrelay"
	"github.com/akkien/aviron/internal/wsgateway"
)

// inClusterNamespaceFile is where every pod's mounted ServiceAccount
// exposes its own namespace — reading it directly avoids a new
// POD_NAMESPACE downward-API env var (dynamic-backend-discovery.md),
// unlike race-service's POD_NAME, which needed the downward API because
// nothing else already exposed that value to the container.
const inClusterNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// shutdownTimeout bounds how long Shutdown waits for in-flight ServeHTTP
// calls (REST proxy requests and WebSocket connections alike) to return
// once SIGTERM arrives — comfortably inside Kubernetes' default 30s
// terminationGracePeriodSeconds; the two must agree once
// k8s-ws-gateway-deploy.md sets the manifest side.
const shutdownTimeout = 25 * time.Second

// connFlushWindow is a short pause between marking this gateway unready
// and force-disconnecting every local WebSocket connection
// (graceful-shutdown.md) — long enough to let any broadcast already in
// flight over the bus reach a raceHub's fan-out and get written out,
// rather than racing a connection's disconnect against its own final
// message.
const connFlushWindow = 500 * time.Millisecond

func Run(cfg wsgateway.Config) {
	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	redisClient, err := redisclient.NewClient(ctx, cfg.RedisURL)
	if err != nil {
		log.Fatalf("redisclient: %v", err)
	}
	defer redisClient.Close()

	// "ws-gateway" is inert here — this process never calls
	// Claim/Refresh/Release/MarkEvicted (all race-service-only), only
	// Owner/SubscribeRoomEvents/IsEvicted.
	locator := roomlocator.NewLocator(redisClient, "ws-gateway")

	natsConn, err := nats.Connect(cfg.NATSURL)
	if err != nil {
		log.Fatalf("nats: %v", err)
	}
	defer natsConn.Close()
	relay := roomrelay.NewBus(natsConn)

	discovery, err := newBackendDiscovery(ctx, cfg, logger)
	if err != nil {
		log.Fatalf("backend discovery: %v", err)
	}

	gw := wsgateway.NewGateway(locator, discovery, cfg.CacheTTL, logger)
	go gw.WatchRoomEvents(ctx)

	hubs := wsgateway.NewRaceHubRegistry(ctx, relay, logger)
	wsHandler := wsgateway.NewWSHandler(locator, relay, hubs, cfg.JWTSecret, cfg.AllowedOrigin, logger)

	gate := &wsgateway.ReadinessGate{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", wsgateway.NewHealthzHandler(redisClient, natsConn, discovery, gate))
	mux.HandleFunc("GET /livez", wsgateway.NewLivezHandler())
	mux.Handle("GET /ws", wsHandler)
	mux.Handle("/", gw)

	httpSrv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go func() {
		logger.Info("ws-gateway listening", slog.String("addr", cfg.ListenAddr), slog.Any("backends", cfg.Backends))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-signalCtx.Done()
	logger.Info("shutdown signal received, marking unready")
	gate.MarkShuttingDown()

	// Give any already-in-flight broadcast a moment to reach local
	// connections before force-disconnecting them below — see
	// connFlushWindow's own comment.
	time.Sleep(connFlushWindow)

	// Force-disconnects every locally-held WebSocket connection first, not
	// after: httpSrv.Shutdown below blocks until every in-flight
	// ServeHTTP call returns, and a WSHandler's call only returns once its
	// connection actually closes — calling Shutdown before this would
	// deadlock, waiting on connections nothing has told to close yet.
	hubs.Shutdown()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown did not complete cleanly", slog.Any("error", err))
	} else {
		logger.Info("shutdown complete")
	}
}

// newBackendDiscovery decides StaticBackends vs. K8sBackendDiscovery by
// whether rest.InClusterConfig succeeds — no new env var
// (dynamic-backend-discovery.md): RACE_SERVICE_INSTANCES stays required
// for local go run/docker-compose (where InClusterConfig always fails)
// and is simply unused in-cluster (where it succeeds).
func newBackendDiscovery(ctx context.Context, cfg wsgateway.Config, logger *slog.Logger) (wsgateway.BackendDiscovery, error) {
	restCfg, err := rest.InClusterConfig()
	if errors.Is(err, rest.ErrNotInCluster) {
		logger.Info("wsgateway: not running in-cluster, using static RACE_SERVICE_INSTANCES")
		return wsgateway.StaticBackends(cfg.Backends), nil
	}
	if err != nil {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("build k8s clientset: %w", err)
	}

	nsBytes, err := os.ReadFile(inClusterNamespaceFile)
	if err != nil {
		return nil, fmt.Errorf("read in-cluster namespace: %w", err)
	}
	namespace := strings.TrimSpace(string(nsBytes))

	discovery, err := wsgateway.NewK8sBackendDiscovery(ctx, clientset, namespace, logger)
	if err != nil {
		return nil, fmt.Errorf("start k8s backend discovery: %w", err)
	}

	logger.Info("wsgateway: running in-cluster, using dynamic EndpointSlice discovery", slog.String("namespace", namespace))

	// Blocks startup until the informer's first List completes — this
	// process must not start serving room-less traffic against an empty
	// backend pool (dynamic-backend-discovery.md's "Readiness: don't
	// accept traffic before the first list lands").
	if !discovery.WaitForSync(ctx) {
		return nil, fmt.Errorf("k8s backend discovery: informer sync did not complete")
	}

	return discovery, nil
}

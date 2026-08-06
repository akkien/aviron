package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/akkien/aviron/internal/config"
	"github.com/akkien/aviron/internal/consumer"
	"github.com/akkien/aviron/internal/db"
	"github.com/akkien/aviron/internal/kafka"
	"github.com/akkien/aviron/internal/metrics"
	"github.com/akkien/aviron/internal/postgres"
)

func Run(cfg *config.Config) {
	// internal/consumer's fetch loops already check ctx.Err() and flush
	// their in-flight batch before returning (graceful-shutdown.md) — the
	// only thing missing was a context that ever actually gets cancelled.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	brokers := strings.Split(cfg.KafkaBrokers, ",")
	producer := kafka.NewProducer(brokers, logger)
	defer producer.Close()

	writer := postgres.NewWorkoutSampleRepository(pool)
	reconciler := postgres.NewRaceRepository(pool)

	m := metrics.NewConsumerMetrics()
	c := consumer.NewConsumer(brokers, writer, reconciler, producer, m, logger)
	m.RegisterLagGauge(c)

	// This binary has no HTTP surface otherwise (no Service in
	// deploy/k8s/consumer today) — this server exists purely to serve
	// /metrics and /debug/pprof/* (metrics/metrics-parity.md).
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	if cfg.PprofEnabled {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	metricsSrv := &http.Server{Addr: cfg.ConsumerListenAddr, Handler: mux}
	go func() {
		logger.Info("consumer metrics listening", slog.String("addr", cfg.ConsumerListenAddr))
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("consumer metrics listen: %v", err)
		}
	}()
	defer metricsSrv.Close()

	logger.Info("consumer starting", slog.Any("brokers", brokers))
	if err := c.Run(ctx); err != nil {
		log.Fatalf("consumer: %v", err)
	}
}

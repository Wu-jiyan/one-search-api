// Command onesearch serves a self-hosted, DeepSeek-compatible search and fetch
// API backed by free search sources.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/wujiyan/one-search-api/internal/api"
	"github.com/wujiyan/one-search-api/internal/auth"
	"github.com/wujiyan/one-search-api/internal/config"
	"github.com/wujiyan/one-search-api/internal/fetchsvc"
	"github.com/wujiyan/one-search-api/internal/limit"
	"github.com/wujiyan/one-search-api/internal/obs"
	"github.com/wujiyan/one-search-api/internal/provider"
	"github.com/wujiyan/one-search-api/internal/search"
)

// version is overridable with -ldflags "-X main.version=...".
var version = "0.1.0"

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML configuration file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("one-search-api " + version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(1)
	}

	logger := obs.NewLogger(cfg.Log.Level, cfg.Log.Format)
	metrics := obs.NewMetrics()

	sources := make([]*search.Source, 0, len(cfg.Providers))
	for _, pc := range cfg.Providers {
		if !pc.IsEnabled() {
			logger.Info("provider disabled", "provider", pc.Name)
			continue
		}
		prov, perr := provider.New(pc, cfg.Fetch.UserAgent)
		if perr != nil {
			logger.Error("provider initialization failed", "provider", pc.Name, "error", perr.Error())
			os.Exit(1)
		}
		timeout := pc.Timeout.D()
		if timeout <= 0 {
			timeout = cfg.Search.ProviderTimeout.D()
		}
		sources = append(sources, &search.Source{
			Name:    pc.Name,
			Type:    pc.Type,
			Weight:  pc.Weight,
			Timeout: timeout,
			Prov:    prov,
			Bucket:  limit.NewBucket(pc.RPM, 0),
			Breaker: limit.NewBreaker(5, 30*time.Second),
		})
	}
	if len(sources) == 0 {
		fmt.Fprintln(os.Stderr, "configuration error: no enabled providers")
		os.Exit(1)
	}

	// Warm browser sessions (Bing) in the background so the first user request
	// does not pay for the homepage cookie round-trip.
	for _, src := range sources {
		if w, ok := src.Prov.(provider.Warm); ok {
			go func(name string, w provider.Warm) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				start := time.Now()
				w.Warm(ctx)
				logger.Debug("provider warm-up finished", "provider", name, "elapsed_ms", time.Since(start).Milliseconds())
			}(src.Name, w)
		}
	}

	fetchService := fetchsvc.New(cfg, metrics)
	searchService := search.New(cfg, sources, fetchService, metrics, logger)
	authn := auth.New(cfg.Auth)
	handler := api.New(cfg, authn, searchService, fetchService, metrics, logger, version).Handler()

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.D(),
		ReadTimeout:       cfg.Server.RequestTimeout.D() + 5*time.Second,
		WriteTimeout:      cfg.Server.RequestTimeout.D() + 10*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	names := make([]string, 0, len(sources))
	for _, src := range sources {
		names = append(names, src.Name)
	}
	logger.Info("one-search-api starting",
		"version", version,
		"addr", cfg.Server.Addr,
		"auth_mode", cfg.Auth.Mode,
		"providers", strings.Join(names, ","),
	)

	errc := make(chan error, 1)
	go func() {
		errc <- srv.ListenAndServe()
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	select {
	case s := <-sig:
		logger.Info("shutdown signal received", "signal", s.String())
	case e := <-errc:
		if e != nil && e != http.ErrServerClosed {
			logger.Error("server failed", "error", e.Error())
			os.Exit(1)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout.D())
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("stopped")
}

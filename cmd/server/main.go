// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied. See the License for the
// specific language governing permissions and limitations
// under the License.

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/wso2/fhir-server/internal/config"
	"github.com/wso2/fhir-server/internal/db"
	"github.com/wso2/fhir-server/internal/handler"
	"github.com/wso2/fhir-server/internal/ig"
	"github.com/wso2/fhir-server/internal/searchparam"
	"github.com/wso2/fhir-server/internal/seed"
	"github.com/wso2/fhir-server/internal/store"
	"github.com/wso2/fhir-server/internal/terminology"
)

func main() {
	if err := run(); err != nil {
		slog.Error("startup failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var configPath string
	flag.StringVar(&configPath, "config", "", "Path to YAML config file (overrides FHIR_SERVER_CONFIG env var)")
	flag.StringVar(&configPath, "c", "", "Path to YAML config file (shorthand for -config)")
	flag.Parse()
	if configPath == "" {
		configPath = os.Getenv("FHIR_SERVER_CONFIG")
	}

	cfg, err := config.LoadFromPath(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	setupLogging(cfg.LogLevel)

	if configPath != "" {
		slog.Info("loaded config file", "path", configPath)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Database
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()
	slog.Info("connected to database")

	// Table creation is opt-in: it needs a DB role with DDL privileges, which
	// the runtime role usually should not have. When disabled (the default),
	// the tables are expected to already exist.
	if cfg.CreateTables {
		slog.Info("creating database tables")
		if err := db.CreateTables(ctx, pool); err != nil {
			return fmt.Errorf("create tables: %w", err)
		}
	} else {
		slog.Info("skipping database table creation; expecting tables to already exist " +
			"(set FHIR_CREATE_TABLES=true or database.createTables to create them)")
	}

	// Seed standard FHIR R4 search parameters (idempotent — ON CONFLICT DO NOTHING)
	if err := seed.SeedSearchParams(ctx, pool); err != nil {
		slog.Warn("search param seed failed (non-fatal)", "err", err)
	}

	// Search param registry — loads base + already-recorded IG params from DB
	registry := searchparam.NewRegistry()
	if err := registry.Load(ctx, pool); err != nil {
		return fmt.Errorf("load search params: %w", err)
	}

	// Store + HTTP (server starts immediately; IGs load in background)
	storeOpts := []func(*store.Store){}
	if tc := terminology.New(cfg.TerminologyURL); tc != nil {
		storeOpts = append(storeOpts, store.WithTerminology(tc))
		slog.Info("terminology server configured", "url", cfg.TerminologyURL)
	}
	s := store.New(pool, registry, storeOpts...)

	// igReady is set to 1 once all IGs finish loading.
	var igReady atomic.Int32
	if len(cfg.IGPackages) == 0 {
		igReady.Store(1)
	}

	router := handler.NewRouter(s, pool, registry, cfg.BaseURL, &igReady, cfg.ValidateOnWrite)
	slog.Info("FHIR router initialized", "baseURL", cfg.BaseURL, "validateOnWrite", cfg.ValidateOnWrite, "igPackages", len(cfg.IGPackages))

	// Timeouts are configurable (SERVER_READ/WRITE/IDLE_TIMEOUT or server.*Timeout
	// in the config file): WriteTimeout bounds the whole handler execution, so the
	// default can cut long-but-legitimate requests (e.g. multi-MB transaction
	// bundles) AFTER they committed. Deployments ingesting large bundles should
	// raise it; 0 disables.
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}
	slog.Info("HTTP server timeouts configured",
		"readTimeout", cfg.ReadTimeout.String(), "writeTimeout", cfg.WriteTimeout.String(), "idleTimeout", cfg.IdleTimeout.String())

	// Start listening before IGs are loaded so liveness probes pass immediately
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("server listening", "addr", srv.Addr, "baseURL", cfg.BaseURL)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			cancel()
		}
	}()

	// Load IGs in the background — parallel downloads, one DB tx per package
	if len(cfg.IGPackages) > 0 {
		go func() {
			igOpts := ig.LoadOptions{
				RegistryURL: cfg.IGRegistryURL,
				ForceReload: cfg.IGForceReload,
				CacheDir:    cfg.IGCacheDir,
			}

			g, gctx := errgroup.WithContext(ctx)
			for _, spec := range cfg.IGPackages {
				spec := spec // capture
				g.Go(func() error {
					result, err := ig.LoadPackage(gctx, pool, registry, spec, igOpts)
					if err != nil {
						slog.Warn("IG package load failed (non-fatal)", "package", spec, "err", err)
						return nil // non-fatal: don't block other packages
					}
					if !result.AlreadyLoaded {
						slog.Info("IG package loaded",
							"package", spec,
							"searchParams", result.SearchParams,
							"profiles", result.Profiles,
						)
					}
					return nil
				})
			}

			if err := g.Wait(); err != nil {
				slog.Warn("IG loading encountered errors", "err", err)
			}

			igReady.Store(1)
			slog.Info("all IG packages ready")
		}()
	}

	select {
	case <-quit:
		slog.Info("shutdown signal received")
	case <-ctx.Done():
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	return srv.Shutdown(shutdownCtx)
}

func setupLogging(level string) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})))
}

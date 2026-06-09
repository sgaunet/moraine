// Command moraine scans a directory of photos, groups them into events,
// classifies each group and serves a sober local web UI to review and sort
// them onto disk. Single static binary, pure Go, no CGo.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/cluster"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/exifmeta"
	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/scan"
	"github.com/sgaunet/moraine/internal/server"
	"github.com/sgaunet/moraine/internal/store"
	"github.com/sgaunet/moraine/internal/thumb"
	"github.com/sgaunet/moraine/web"
)

// Exit codes follow the CLI contract: 0 success, 1 runtime error, 2 usage error.
const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "erreur d'arguments :", err)
		fmt.Fprintln(os.Stderr, "usage : moraine [flags] <dossier-source>")
		os.Exit(exitUsage)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "erreur :", err)
		os.Exit(exitRuntime)
	}
	if err := run(cfg, logger); err != nil {
		fmt.Fprintln(os.Stderr, "erreur :", err)
		os.Exit(exitRuntime)
	}
	os.Exit(exitOK)
}

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := buildStore(ctx, cfg, logger)
	if err != nil {
		return err
	}

	placeholder, err := web.FS.ReadFile("placeholder.svg")
	if err != nil {
		return fmt.Errorf("placeholder embarqué illisible : %w", err)
	}
	thumbDir := filepath.Join(os.TempDir(), "moraine-thumbs")
	cache, err := thumb.NewCache(thumbDir, placeholder)
	if err != nil {
		return err
	}

	srv := server.New(st, cache, logger)
	logger.Info("interface prête", "url", "http://"+cfg.Addr)
	if err := srv.Start(ctx, cfg.Addr); err != nil {
		return fmt.Errorf("serveur : %w", err)
	}
	logger.Info("arrêt propre")
	return nil
}

// buildStore runs the start-up pipeline: scan → exif → cluster → classify →
// build the in-memory store.
func buildStore(ctx context.Context, cfg config.Config, logger *slog.Logger) (*store.Store, error) {
	found, err := scan.Scan(cfg.Source, cfg.DestRoot)
	if err != nil {
		return nil, err
	}
	logger.Info("scan", "images", len(found), "destination_exclue", cfg.DestRoot)

	photos := readMeta(found, logger)
	logger.Info("exif", "lues", len(photos), "sur", len(found))

	clusters := cluster.Cluster(photos, cfg.Gap)
	logger.Info("cluster", "photos", len(photos), "groupes", len(clusters), "gap", cfg.Gap.String())

	labels := classifyAll(ctx, clusters, cfg, logger)

	st := store.New(cfg.Source, cfg.DestRoot)
	store.BuildFromClusters(st, clusters, labels)
	return st, nil
}

// readMeta reads EXIF metadata for every file using a bounded worker pool.
func readMeta(found []scan.Found, logger *slog.Logger) []photo.Photo {
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		photos = make([]photo.Photo, 0, len(found))
	)
	for _, f := range found {
		wg.Add(1)
		sem <- struct{}{}
		go func(f scan.Found) {
			defer wg.Done()
			defer func() { <-sem }()
			p, err := exifmeta.Read(f.Path, f.Format)
			if err != nil {
				logger.Warn("exif ignorée", "fichier", f.Path, "err", err)
				return
			}
			mu.Lock()
			photos = append(photos, p)
			mu.Unlock()
		}(f)
	}
	wg.Wait()
	return photos
}

// classifyAll labels each cluster (heuristic → Ollama → date fallback).
func classifyAll(ctx context.Context, clusters []photo.Cluster, cfg config.Config, logger *slog.Logger) []string {
	opts := classify.Options{Home: cfg.Home}
	if cfg.Sample > 0 {
		opts.Classifier = classify.NewOllama(cfg.OllamaURL, cfg.Model, cfg.Sample)
	}
	labels := make([]string, len(clusters))
	for i, c := range clusters {
		labels[i] = classify.Label(ctx, c, opts)
	}
	logger.Info("classify", "groupes", len(clusters))
	return labels
}

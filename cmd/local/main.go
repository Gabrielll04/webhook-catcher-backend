package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"webhook-catcher/internal/app"
	"webhook-catcher/internal/infra/config"
	"webhook-catcher/internal/repository"
	"webhook-catcher/internal/repository/memory"
	"webhook-catcher/internal/repository/sqlstore"
)

func main() {
	cfg := config.Load()

	store, cleanup, err := buildStore(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			log.Printf("store cleanup failed: %v", err)
		}
	}()

	h := app.NewWithStore(cfg, store)
	addr := ":" + cfg.Port
	srv := &http.Server{
		Addr:         addr,
		Handler:      h,
		ReadTimeout:  time.Duration(cfg.ReadTimeoutSeconds) * time.Second,
		WriteTimeout: time.Duration(cfg.WriteTimeoutSeconds) * time.Second,
	}

	go func() {
		log.Printf("listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server failed: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}

func buildStore(cfg config.Config) (repository.Store, func() error, error) {
	if cfg.DatabaseURL == "" {
		log.Printf("DATABASE_URL not set, using in-memory store")
		return memory.New(), func() error { return nil }, nil
	}

	db, err := sql.Open("sqlite", cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, nil, err
	}

	if cfg.MigrationsAutoApply {
		if err := applyMigrations(db, cfg.MigrationsDir); err != nil {
			db.Close()
			return nil, nil, err
		}
	}

	return sqlstore.New(db), db.Close, nil
}

func applyMigrations(db *sql.DB, migrationsDir string) error {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)

	for _, file := range files {
		path := filepath.Join(migrationsDir, file)
		sqlBytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			return err
		}
	}
	return nil
}

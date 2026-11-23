package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/0xrinful/InkFetch/internal/queue"
	"github.com/0xrinful/InkFetch/internal/registry"
	"github.com/0xrinful/InkFetch/internal/requester/flare"
	"github.com/0xrinful/InkFetch/internal/server"
	"github.com/0xrinful/InkFetch/internal/service"
	"github.com/0xrinful/InkFetch/internal/storage/db"
	"github.com/0xrinful/InkFetch/internal/storage/files"
	"github.com/joho/godotenv"
	"github.com/lmittmann/tint"
)

func main() {
	logger := slog.New(tint.NewHandler(os.Stdout, &tint.Options{
		Level:      slog.LevelDebug,
		TimeFormat: time.Kitchen,
	}))
	slog.SetDefault(logger)

	if err := godotenv.Load(); err != nil {
		slog.Warn("no .env file found, reading environment variables directly")
	}

	tlsCertPath := os.Getenv("TLS_CERT_PATH")
	tlsKeyPath := os.Getenv("TLS_KEY_PATH")
	if tlsCertPath == "" || tlsKeyPath == "" {
		log.Fatal("missing TLS_CERT_PATH or TLS_KEY_PATH")
	}

	secret := os.Getenv("SECRET")
	if secret == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			log.Fatal(err)
		}
		secret = base64.StdEncoding.EncodeToString(buf)
		slog.Warn("using generated ephemeral session secret")
	}
	server.SetSessionSecret(secret)

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "inkfetch.db"
	} else {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			log.Fatal(err)
		}
	}

	filesRoot := os.Getenv("FILES_ROOT")
	if filesRoot == "" {
		filesRoot = "data"
	}
	if err := os.MkdirAll(filesRoot, 0o755); err != nil {
		log.Fatal(err)
	}

	database, err := db.New(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	fileStore := files.New(filesRoot)

	var solver *flare.Solver
	if envBool("FLARESOLVER_ENABLED", false) {
		solver = flare.NewSolverWithURL(os.Getenv("FLARESOLVER_URL"))
		slog.Info("flaresolver enabled")
	} else {
		slog.Info("flaresolver disabled")
	}

	reg := registry.NewRegistry(solver, database)
	q := queue.NewQueue()
	svc := service.New(reg, database, fileStore, q)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	worker := queue.NewWorkerWithSettings(q, reg, svc.Settings, database, fileStore)
	worker.Start(ctx)

	srv := server.New(svc)
	handler := server.WithLogging(srv)

	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8000"
	}
	addr := ":" + strings.TrimPrefix(port, ":")

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		slog.Info("shutting down InkFetch server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("server shutdown error", "err", err)
		}
	}()

	slog.Info("InkFetch started", "addr", addr)
	if err := httpServer.ListenAndServeTLS(
		tlsCertPath,
		tlsKeyPath,
	); err != nil &&
		!errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}

	worker.Wait()
	slog.Info("InkFetch stopped cleanly")
}

func envBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		slog.Warn(
			"invalid boolean env var, using default",
			"key",
			key,
			"value",
			raw,
			"default",
			fallback,
		)
		return fallback
	}
	return v
}

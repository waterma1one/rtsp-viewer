// Command server streams RTSP sources to browsers as fragmented MP4 over
// WebSockets. Configuration is read from the environment; see README.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/waterma1one/rtsp-viewer/backend/internal/api"
	"github.com/waterma1one/rtsp-viewer/backend/internal/stream"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	port := env("PORT", "8080")
	maxStreams, err := strconv.Atoi(env("MAX_STREAMS", "6"))
	if err != nil || maxStreams < 1 {
		return fmt.Errorf("MAX_STREAMS must be a positive integer")
	}
	idleGrace, err := time.ParseDuration(env("IDLE_GRACE", "30s"))
	if err != nil {
		return fmt.Errorf("IDLE_GRACE: %w", err)
	}
	demos, err := parseDemos(env("DEMO_STREAMS", ""))
	if err != nil {
		return err
	}

	allowedHostPorts := map[string]bool{}
	for _, hp := range splitList(env("ALLOWED_RTSP_HOSTS", "")) {
		allowedHostPorts[hp] = true
	}

	mgr := stream.NewManager(
		stream.FFmpegSource(env("FFMPEG_PATH", "ffmpeg")),
		maxStreams, idleGrace, stream.DefaultOptions(), log,
	)
	srv := &api.Server{
		Manager: mgr,
		Validator: &api.URLValidator{
			AllowPrivate:     env("ALLOW_PRIVATE_NETWORKS", "") == "true",
			AllowedHostPorts: allowedHostPorts,
		},
		Demos:          demos,
		AllowedOrigins: splitList(env("ALLOWED_ORIGINS", "localhost:*,127.0.0.1:*")),
		Log:            log,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpSrv := &http.Server{
		Addr:              net.JoinHostPort("", port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()
	log.Info("listening", "port", port, "maxStreams", maxStreams, "demos", len(demos))

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	// Stop sources first: that ends every WebSocket handler, which lets
	// Shutdown complete instead of waiting on long-lived connections.
	mgr.Shutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseDemos reads "name=url,name=url".
func parseDemos(s string) ([]api.DemoStream, error) {
	var out []api.DemoStream
	for _, item := range splitList(s) {
		name, url, ok := strings.Cut(item, "=")
		if !ok || name == "" || url == "" {
			return nil, fmt.Errorf("DEMO_STREAMS entry %q must be name=url", item)
		}
		out = append(out, api.DemoStream{Name: strings.TrimSpace(name), URL: strings.TrimSpace(url)})
	}
	return out, nil
}

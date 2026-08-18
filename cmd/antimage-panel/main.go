// Command antimage-panel serves the operator API, the UI, and the gRPC
// control plane that agents dial into.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/httpapi"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/shared/secrets"
	"github.com/amyrm/antimage/internal/shared/version"
)

func main() {
	dataDir := flag.String("data-dir", "/var/lib/antimage", "data directory")
	httpAddr := flag.String("http", ":8080", "HTTP listen address")
	grpcAddr := flag.String("grpc", ":8443", "gRPC control listen address")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		_, _ = os.Stdout.WriteString(version.Version + "\n")
		return
	}

	if err := run(*dataDir, *httpAddr, *grpcAddr); err != nil {
		slog.Error("panel stopped", "error", err)
		os.Exit(1)
	}
}

func run(dataDir, httpAddr, grpcAddr string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	key, err := secrets.LoadOrCreateKey(filepath.Join(dataDir, "master.key"))
	if err != nil {
		return err
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dataDir, "antimage.db"))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ca, err := nodes.LoadOrCreateCA(ctx, st, box)
	if err != nil {
		return err
	}
	hub := control.NewHub()
	now := func() time.Time { return time.Now().UTC() }

	go nodes.NewSweeper(st, now).Run(ctx, 30*time.Second)

	deps := control.Deps{Store: st, CA: ca, Hub: hub, Now: now}
	grpcServer := grpc.NewServer()
	pb.RegisterEnrollmentServer(grpcServer, control.NewEnrollmentService(deps))
	pb.RegisterControlServer(grpcServer, control.NewControlService(deps))

	grpcListener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr: httpAddr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Store:    st,
			Sessions: auth.NewSessions(st, now),
			Limiter:  auth.NewLimiter(st, now),
			Hub:      hub,
			CA:       ca,
			// Box is what lets handleLogin decrypt admins.totp_secret_enc.
			// It fails closed: with a nil Box, an admin who has enrolled TOTP
			// is denied rather than admitted on a password alone. Omitting it
			// here would lock every TOTP-enrolled admin out of production
			// while every unit test still passed.
			Box: box,
			// SSEInterval is left at zero on purpose: zero selects the
			// production default. Only tests set it.
			Now: now,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() { errCh <- grpcServer.Serve(grpcListener) }()
	go func() {
		if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	slog.Info("antimage-panel listening",
		"version", version.Version, "http", httpAddr, "grpc", grpcAddr,
		"ca_fingerprint", ca.FingerprintSHA256())

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	grpcServer.GracefulStop()
	return nil
}

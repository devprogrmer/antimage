// Command antimage-node is the agent. It dials the panel, never listens.
package main

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/amyrm/antimage/internal/node/adapterfactory"
	"github.com/amyrm/antimage/internal/node/agent"
	"github.com/amyrm/antimage/internal/shared/version"
)

func main() {
	configPath := flag.String("config", "/etc/antimage/node.yaml", "path to node.yaml")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Version)
		return
	}

	cfg, err := agent.LoadConfig(*configPath)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cert, caDER, nodeID, err := loadOrEnroll(ctx, cfg, *configPath)
	if err != nil {
		slog.Error("enrollment", "error", err)
		os.Exit(1)
	}
	cfg.NodeID = nodeID

	// The adapters this host declares in node.yaml. A node serves several
	// protocols at once, each handling the services of its own kind over the
	// one desired document.
	ads, err := adapterfactory.Build(cfg)
	if err != nil {
		slog.Error("build adapter registry", "error", err)
		os.Exit(1)
	}
	kinds := make([]string, 0, ads.Len())
	for _, d := range ads.Descriptors() {
		kinds = append(kinds, string(d.Kind))
	}
	if adapterfactory.NoAdaptersConfigured(cfg) {
		// Not fatal -- an enrolled but unprovisioned host is a real state, and
		// refusing to start would strand it. But a node that quietly serves
		// nothing is exactly the failure worth being loud about.
		slog.Warn("no adapters configured in node.yaml; this node will serve no traffic",
			"supported", strings.Join(adapterfactory.SupportedKinds(), ", "))
	}
	client := agent.NewClient(cfg, ads, agent.SystemClock{}, cert, caDER)

	slog.Info("antimage-node starting", "version", version.Version, "node_id", nodeID,
		"adapters", strings.Join(kinds, ","))
	if err := client.Run(ctx); err != nil && ctx.Err() == nil {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

// loadOrEnroll reuses an existing certificate when present, so a restart does
// not consume a new enrollment token.
func loadOrEnroll(ctx context.Context, cfg *agent.Config, configPath string) (tls.Certificate, []byte, int64, error) {
	certPath := filepath.Join(cfg.StateDir, "node.crt")
	keyPath := filepath.Join(cfg.StateDir, "node.key")
	caPath := filepath.Join(cfg.StateDir, "panel-ca.crt")

	if certPEM, err := os.ReadFile(certPath); err == nil {
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			return tls.Certificate{}, nil, 0, fmt.Errorf("read node key: %w", err)
		}
		caPEM, err := os.ReadFile(caPath)
		if err != nil {
			return tls.Certificate{}, nil, 0, fmt.Errorf("read panel CA: %w", err)
		}
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return tls.Certificate{}, nil, 0, fmt.Errorf("load node keypair: %w", err)
		}
		block, _ := pem.Decode(caPEM)
		if block == nil {
			return tls.Certificate{}, nil, 0, errors.New("panel CA file holds no PEM block")
		}
		return pair, block.Bytes, cfg.NodeID, nil
	}

	pair, caDER, nodeID, err := agent.Enroll(ctx, cfg)
	if err != nil {
		return tls.Certificate{}, nil, 0, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		return tls.Certificate{}, nil, 0, fmt.Errorf("write panel CA: %w", err)
	}
	// Burn the token from disk: it is single-use and now spent.
	cfg.Token = ""
	cfg.NodeID = nodeID
	if err := cfg.Save(configPath); err != nil {
		return tls.Certificate{}, nil, 0, err
	}
	return pair, caDER, nodeID, nil
}

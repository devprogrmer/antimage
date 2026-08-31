// Command antimage-panel serves the operator API, the UI, and the gRPC
// control plane that agents dial into.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/deployment"
	"github.com/amyrm/antimage/internal/panel/httpapi"
	"github.com/amyrm/antimage/internal/panel/metrics"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/notify/telegram"
	"github.com/amyrm/antimage/internal/panel/observability"
	"github.com/amyrm/antimage/internal/panel/resellers"
	"github.com/amyrm/antimage/internal/panel/service"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/panel/subjects"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/shared/secrets"
	"github.com/amyrm/antimage/internal/shared/version"
)

func main() {
	dataDir := flag.String("data-dir", "/var/lib/antimage", "data directory")
	httpAddr := flag.String("http", ":8080", "HTTP listen address")
	grpcAddr := flag.String("grpc", ":8443", "gRPC control listen address")
	grpcHosts := flag.String("grpc-hosts", "localhost,127.0.0.1",
		"comma-separated DNS names and IPs agents dial this panel on; they become the SANs of the panel's TLS certificate")
	showVersion := flag.Bool("version", false, "print version and exit")
	// Accounting maintenance, for use either side of a migration. Read-only
	// unless --repair is given, and --repair reports what it would do rather
	// than doing it unless --apply is given too: recomputing a billing figure
	// is not something to trigger by typo.
	verifyAccounting := flag.Bool("accounting-verify", false,
		"print an accounting checksum and exit")
	repairAccounting := flag.Bool("accounting-repair", false,
		"recompute hourly rollups inflated by the pre-00026 sweeper; reports without writing unless --accounting-apply is given")
	applyRepair := flag.Bool("accounting-apply", false,
		"with --accounting-repair, write the repair instead of reporting it")
	flag.Parse()

	if *showVersion {
		_, _ = os.Stdout.WriteString(version.Version + "\n")
		return
	}

	if *verifyAccounting || *repairAccounting {
		if err := accountingMaintenance(*dataDir, *repairAccounting, *applyRepair); err != nil {
			slog.Error("accounting maintenance failed", "error", err)
			os.Exit(1)
		}
		return
	}

	if err := run(*dataDir, *httpAddr, *grpcAddr, *grpcHosts); err != nil {
		slog.Error("panel stopped", "error", err)
		os.Exit(1)
	}
}

// accountingMaintenance runs the checksum, and optionally the rollup repair,
// against the database without starting the panel.
//
// Taking the checksum either side of a migration is how "this migration changes
// no bill" stops being a claim and becomes a check. The repair is separate and
// opt-in because it rewrites billing figures: it recomputes the hourly rollups
// that the pre-00026 sweeper inflated, and only for the buckets whose raw
// deltas survive the retention window. Buckets whose deltas have been pruned
// are reported and left exactly as they are, because the true figure is not
// recoverable and a confident wrong number is worse than a known-wrong one.
func accountingMaintenance(dataDir string, repair, apply bool) error {
	st, err := store.Open(filepath.Join(dataDir, "antimage.db"))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	before, err := nodes.TakeChecksum(ctx, st)
	if err != nil {
		return err
	}
	_, _ = os.Stdout.WriteString(before.String())

	if !repair {
		return nil
	}

	report, err := nodes.RepairHourlyRollups(ctx, st, !apply)
	if err != nil {
		return err
	}
	_, _ = os.Stdout.WriteString(report.String() + "\n")
	if !apply {
		_, _ = os.Stdout.WriteString(
			"nothing was written; re-run with --accounting-apply to apply this\n")
		return nil
	}

	after, err := nodes.TakeChecksum(ctx, st)
	if err != nil {
		return err
	}
	for _, line := range before.Divergence(after) {
		_, _ = os.Stdout.WriteString("changed " + line + "\n")
	}
	return nil
}

func run(dataDir, httpAddr, grpcAddr, grpcHostList string) error {
	// Agents verify the panel's certificate against the name they dialled, so
	// these have to be the names operators actually put in node.yaml. Getting
	// it wrong surfaces as a TLS failure on every node at once, which is loud
	// -- but the warning below is cheaper than discovering it that way.
	var grpcHosts []string
	for _, h := range strings.Split(grpcHostList, ",") {
		if h = strings.TrimSpace(h); h != "" {
			grpcHosts = append(grpcHosts, h)
		}
	}
	if len(grpcHosts) == 0 {
		return errors.New("--grpc-hosts must name at least one host agents will dial")
	}
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

	// Recover any deployments stuck in in_progress state from process crashes
	orchestrator := deployment.NewOrchestrator(st)
	if err := orchestrator.RecoverStaleDeployments(ctx); err != nil {
		slog.Warn("deployment recovery failed", "error", err)
	}

	// SP5: Register Prometheus metrics collector
	collector := metrics.NewCollector(st)
	prometheus.MustRegister(collector)

	ca, err := nodes.LoadOrCreateCA(ctx, st, box)
	if err != nil {
		return err
	}
	hub := control.NewHub()
	now := func() time.Time { return time.Now().UTC() }

	go nodes.NewSweeper(st, now).Run(ctx, 30*time.Second)

	// Deployment timeout enforcement sweeper
	// Runs every 2 minutes to detect and fail deployments exceeding their timeout
	timeoutConfig := deployment.DefaultTimeoutConfig()
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := orchestrator.EnforceTimeouts(ctx, timeoutConfig, now()); err != nil {
					slog.ErrorContext(ctx, "deployment timeout enforcement failed", "error", err)
				}
			}
		}
	}()

	// Expiry is enforced by omission from the desired document; this sweeper
	// makes it prompt and visible by stamping expired_at and bumping the
	// revision of every affected node, rather than leaving the removal to
	// whenever some unrelated change next occurs.
	go subjects.NewSweeper(st, now, func(nodeID, revision int64) { hub.Notify(nodeID, revision) }, nodes.WithUnsealer(box)).
		Run(ctx, time.Minute)

	// SP3: quota enforcement sweeper finds subjects over quota and freezes them.
	// Interval: 5 minutes (frequent enough for prompt enforcement, infrequent enough to avoid database load).
	quotaEnforcer := &nodes.QuotaEnforcementSweeper{
		Store: st,
		Log:   slog.Default(),
		CommitFunc: func(ctx context.Context, nodeID int64, actor, reason string) error {
			_, err := nodes.CommitNodeChange(ctx, st, nodeID, audit.SystemActor(actor), "", reason, nil, nodes.WithUnsealer(box))
			return err
		},
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := quotaEnforcer.Run(ctx, now().Unix()); err != nil {
					slog.ErrorContext(ctx, "quota enforcement sweep failed", "error", err)
				}
			}
		}
	}()

	// SP3: quota reset sweeper finds subjects past their reset time and resets usage.
	// Interval: hourly (resets are timestamp-based, no need for high frequency).
	quotaResetter := &nodes.QuotaResetSweeper{
		Store: st,
		Log:   slog.Default(),
		CommitFunc: func(ctx context.Context, nodeID int64, actor, reason string) error {
			_, err := nodes.CommitNodeChange(ctx, st, nodeID, audit.SystemActor(actor), "", reason, nil, nodes.WithUnsealer(box))
			return err
		},
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := quotaResetter.Run(ctx, now().Unix()); err != nil {
					slog.ErrorContext(ctx, "quota reset sweep failed", "error", err)
				}
			}
		}
	}()

	// SP3: rollup jobs aggregate raw deltas into hourly and daily buckets.
	// Hourly rollup: runs every hour at :15 past to catch deltas from the previous hour.
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		// Initial run after 15 minutes to catch any backlog.
		time.Sleep(15 * time.Minute)
		if err := nodes.RollupHourly(ctx, st, now().Unix()); err != nil {
			slog.ErrorContext(ctx, "initial hourly rollup failed", "error", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := nodes.RollupHourly(ctx, st, now().Unix()); err != nil {
					slog.ErrorContext(ctx, "hourly rollup failed", "error", err)
				}
			}
		}
	}()

	// Daily rollup: runs once per day at 00:30 to aggregate the previous day.
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		// Calculate initial delay to 00:30 UTC.
		nowTime := now()
		next := time.Date(nowTime.Year(), nowTime.Month(), nowTime.Day(), 0, 30, 0, 0, time.UTC)
		if nowTime.After(next) {
			next = next.Add(24 * time.Hour)
		}
		initialDelay := next.Sub(nowTime)
		time.Sleep(initialDelay)
		if err := nodes.RollupDaily(ctx, st, now().Unix()); err != nil {
			slog.ErrorContext(ctx, "initial daily rollup failed", "error", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := nodes.RollupDaily(ctx, st, now().Unix()); err != nil {
					slog.ErrorContext(ctx, "daily rollup failed", "error", err)
				}
			}
		}
	}()

	// SP3: prune old raw deltas after 7 days (design decision 6: raw deltas brief, rollups indefinite).
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		const retentionDays = 7
		const retentionSeconds = retentionDays * 24 * 60 * 60
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				deleted, err := nodes.PruneUsageDeltas(ctx, st, retentionSeconds, now().Unix())
				if err != nil {
					slog.ErrorContext(ctx, "prune usage deltas failed", "error", err)
				} else if deleted > 0 {
					slog.InfoContext(ctx, "pruned old usage deltas", "count", deleted)
				}
			}
		}
	}()

	// SP7: Certificate and quota alert sweeper
	// Runs every 5 minutes to check for expiring certificates and quota thresholds
	obsSweeper := observability.NewSweeper(st)
	go obsSweeper.Run(ctx)

	// SP7: Hourly rollup generator for observability metrics
	// Aggregates detailed node_health data into hourly summaries
	obsRollup := observability.NewRollupGenerator(st)
	go obsRollup.RunHourly(ctx)

	// SP7: Daily rollup generator for observability metrics
	// Aggregates hourly data into daily summaries
	go obsRollup.RunDaily(ctx)

	// Telegram bot.
	//
	// The token comes from the environment, never a flag: a flag is visible in
	// `ps` output to every user on the box, and a bot token lets its holder
	// impersonate the panel to every linked operator.
	//
	// An absent token means the bot is simply off, which is the right default:
	// an operator who has not configured Telegram should not have a component
	// polling an external service on their behalf.
	if token := os.Getenv("ANTIMAGE_TELEGRAM_TOKEN"); token != "" {
		links := telegram.NewStore(st, now)

		// The bot reads through the same service layer the HTTP API uses, so
		// its commands inherit permission checks, tenant scope and audit
		// rather than reimplementing them.
		subjStore := subjects.NewStore(st, box, now)
		botSvc := service.NewSubjects(
			st, subjStore, resellers.NewStore(st, subjStore, now),
			hub, now, nodes.WithUnsealer(box),
		)

		// Optional. Without it /config returns the subscription path and says
		// what to prefix it with, rather than emitting a link built from a
		// guessed hostname that would not resolve.
		publicURL := os.Getenv("ANTIMAGE_PUBLIC_URL")

		bot := telegram.NewBot(telegram.NewHTTPAPI(token), st, links, botSvc, publicURL, now)
		go bot.Run(ctx)
	} else {
		slog.InfoContext(ctx, "telegram bot disabled",
			"reason", "ANTIMAGE_TELEGRAM_TOKEN is not set")
	}

	// The control plane is mTLS end to end. Without credentials here the
	// server speaks plaintext HTTP/2 while both agent paths dial with TLS, so
	// every handshake fails before control.VerifyPeer is ever reached and no
	// node can enroll or stream.
	serverCert, err := ca.IssueServerCert(grpcHosts, now())
	if err != nil {
		return fmt.Errorf("issue panel server certificate: %w", err)
	}
	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS13,
		// VerifyClientCertIfGiven, not RequireAndVerifyClientCert: enrolment
		// necessarily happens before the node holds any certificate, so
		// requiring one would make enrolment impossible. The Control service
		// enforces the requirement per-RPC through VerifyPeer, which also
		// checks the fingerprint against the nodes allow-list -- something a
		// listener-level setting cannot do.
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  ca.ClientCAPool(),
	})

	// Box lets the control service unseal subject credentials when it builds a
	// desired document. Without it a node that has subjects cannot fetch a
	// snapshot at all -- deliberately, since the alternative is serving one
	// that omits every subject.
	deps := control.Deps{Store: st, CA: ca, Hub: hub, Now: now, Box: box}
	grpcServer := grpc.NewServer(grpc.Creds(creds))
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
			// Agent binaries are published here for install.sh to fetch.
			DownloadDir: filepath.Join(dataDir, "downloads"),
			// SSEInterval is left at zero on purpose: zero selects the
			// production default. Only tests set it.
			Now:          now,
			CoreVersions: httpapi.NewXrayCoreVersionCache(),
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
		"ca_fingerprint", ca.FingerprintSHA256(),
		// Printed because a SAN mismatch is the single most likely reason a
		// fleet fails to connect, and it is invisible until an agent tries.
		"grpc_cert_hosts", grpcHosts)

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

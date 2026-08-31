// Command antimage-ctl is the local administration and recovery path for the
// antimage panel. When the UI is unreachable or every admin is locked out,
// these commands are the way back in, so they talk to the database directly
// rather than through the HTTP API.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/version"
)

const usage = `antimage-ctl — local administration for the antimage panel

Usage:
  antimage-ctl [--data-dir DIR] <command> [arguments]

Commands:
  create-admin   USERNAME PASSWORD ROLE   create an admin (roles: super_admin, admin, reseller, readonly)
  reset-password USERNAME PASSWORD        set a new password and revoke that admin's sessions
  list-admins                             list admins with their roles
  set-delete-cap USERNAME BYTES|none      refuse this admin deletion of a customer past this much traffic
  enroll-token   NODE_ID                  print a single-use enrollment token
  backup         DEST.db                  write a consistent database copy
  version                                 print the version
`

func main() {
	dataDir := flag.String("data-dir", "/var/lib/antimage", "data directory")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	// Handled before opening the database so `version` works on a host that
	// has no panel data yet.
	if args[0] == "version" {
		fmt.Println(version.Version)
		return
	}

	s, err := store.Open(filepath.Join(*dataDir, "antimage.db"))
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: open database: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = s.Close() }()

	if err := dispatch(context.Background(), s, args, os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func dispatch(ctx context.Context, s *store.Store, args []string, out *os.File) error {
	switch args[0] {
	case "create-admin":
		if len(args) != 4 {
			return fmt.Errorf("usage: create-admin USERNAME PASSWORD ROLE")
		}
		if err := createAdmin(ctx, s, args[1], args[2], args[3]); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "created admin %q with role %q\n", args[1], args[3])
		return nil

	case "reset-password":
		if len(args) != 3 {
			return fmt.Errorf("usage: reset-password USERNAME PASSWORD")
		}
		if err := resetPassword(ctx, s, args[1], args[2]); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "password reset for %q; all their sessions were revoked\n", args[1])
		return nil

	case "list-admins":
		return listAdmins(ctx, s, out)

	case "set-delete-cap":
		if len(args) != 3 {
			return fmt.Errorf("usage: set-delete-cap USERNAME BYTES|none")
		}
		shown, err := setDeleteCap(ctx, s, args[1], args[2])
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "delete cap for %q is now %s\n", args[1], shown)
		return nil

	case "enroll-token":
		if len(args) != 2 {
			return fmt.Errorf("usage: enroll-token NODE_ID")
		}
		nodeID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid node id %q", args[1])
		}
		token, err := nodes.IssueEnrollToken(ctx, s, nodeID, ctlActor(), "", time.Now().UTC())
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(out, token)
		return nil

	case "backup":
		if len(args) != 2 {
			return fmt.Errorf("usage: backup DEST.db")
		}
		if err := backup(ctx, s, args[1]); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "backup written to %s\n", args[1])
		return nil

	default:
		return fmt.Errorf("unknown command %q; run with --help", args[0])
	}
}

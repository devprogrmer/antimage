package nodes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/store"
)

// CommitResult reports what a commit did. Changed is false when the mutation
// left the canonical document identical, in which case Revision is unchanged.
type CommitResult struct {
	Changed  bool
	Revision int64
	SHA256   string
}

// CommitNodeChange is the ONLY path that may alter a node's desired document.
//
// It implements spec invariants 1, 2, 4, and 5 together, which is why they are
// structural rather than a checklist:
//
//  1. the mutation, the revision bump, and the revision row share one transaction
//  2. the revision advances only when the canonical hash actually changes
//  4. doc_sha256 is computed from the exact bytes of that revision's document
//  5. the snapshot comes from BuildDesiredSnapshot, never assembled by callers
//
// Callers pass a mutate function that performs their writes on the supplied
// transaction. They must not touch nodes.desired_revision or node_revisions.
//
// "No-op" means identical to the last established revision, not identical to
// nothing. A node's first-ever commit therefore always creates revision 1,
// even if mutate makes no semantic change: there is no revision 0 row to
// compare against (node_revisions.revision has CHECK (revision > 0)), so the
// first comparison is against the empty string and never matches.
func CommitNodeChange(
	ctx context.Context,
	s *store.Store,
	nodeID int64,
	actor audit.Actor,
	requestID string,
	reason string,
	mutate func(*sql.Tx) error,
	opts ...SnapshotOption,
) (*CommitResult, error) {
	var result CommitResult

	err := s.Write(ctx, func(tx *sql.Tx) error {
		if mutate != nil {
			if err := mutate(tx); err != nil {
				return err
			}
		}

		// Rebuild inside the same transaction so the hash describes the
		// post-mutation state exactly.
		snap, err := BuildDesiredSnapshot(ctx, tx, nodeID, opts...)
		if err != nil {
			return err
		}

		var previous string
		err = tx.QueryRowContext(ctx,
			`SELECT doc_sha256 FROM node_revisions
			  WHERE node_id = ? ORDER BY revision DESC LIMIT 1`, nodeID).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read previous revision hash: %w", err)
		}

		if previous == snap.SHA256 {
			// Semantically identical: no revision, no fan-out, no reconcile.
			result = CommitResult{Changed: false, Revision: snap.Revision, SHA256: snap.SHA256}
			return nil
		}

		next := snap.Revision + 1

		// The document embeds its own revision, so the bytes hashed above
		// describe revision N while this row is N+1. Rebuild after bumping
		// so the stored hash matches what the agent will actually receive.
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET desired_revision = ? WHERE id = ?`, next, nodeID); err != nil {
			return fmt.Errorf("bump desired_revision: %w", err)
		}
		final, err := BuildDesiredSnapshot(ctx, tx, nodeID, opts...)
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO node_revisions
			   (node_id, revision, created_at, actor_type, actor_admin_id,
			    actor_label, reason, doc_sha256)
			 VALUES (?,?,?,?,?,?,?,?)`,
			nodeID, next, time.Now().UTC().Unix(),
			string(actor.Type), actor.AdminID, actor.Label, reason, final.SHA256,
		); err != nil {
			return fmt.Errorf("insert node revision: %w", err)
		}

		if err := audit.InTx(ctx, tx, requestID, actor, audit.Record{
			Action:     "node.revision",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			After:      map[string]any{"revision": next, "sha256": final.SHA256, "reason": reason},
			Result:     "ok",
		}); err != nil {
			return err
		}

		result = CommitResult{Changed: true, Revision: next, SHA256: final.SHA256}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

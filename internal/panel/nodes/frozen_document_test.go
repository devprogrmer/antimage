package nodes

import (
	"context"
	"database/sql"
	"testing"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

// A frozen subject must not appear in the desired document.
//
// service.Subjects.SetFrozen documents freezing as a revocation -- "a frozen
// user who stays connected is not frozen" -- and subjects.Store.Freeze writes
// frozen_at without touching enabled. buildSubjects filters on enabled and
// expires_at only, so the frozen subject is rebuilt straight back into the
// document. SetFrozen even republishes correctly through CommitNodeChange, so
// the node is promptly handed a NEW revision that still contains the user the
// operator just revoked.
//
// Subject.Active() carries the same gap: it is documented as "the single
// predicate the document builder and the expiry sweeper both consult, so they
// cannot disagree about who is entitled to service", and it does not consider
// frozen_at at all.

// plainUnsealer returns the sealed bytes unchanged. The document builder
// refuses to run without an unsealer -- it will not silently deprovision
// subjects it cannot decrypt -- and this test is about which subjects are
// selected, not about how their credentials are protected.
type plainUnsealer struct{}

func (plainUnsealer) Open(sealed []byte) ([]byte, error) { return sealed, nil }

// frozenSubjectOnNode seeds a node with one service and one subject granted to
// it, then freezes the subject the way the admin path does: frozen_at set,
// enabled untouched.
func frozenSubjectOnNode(t *testing.T, st *store.Store) (nodeID, subjectID int64) {
	t.Helper()
	ctx := context.Background()
	err := st.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES ('n1', '127.0.0.1', 1000)`)
		if err != nil {
			return err
		}
		nodeID, _ = r.LastInsertId()

		r, err = tx.ExecContext(ctx,
			`INSERT INTO services (node_id, adapter_kind, enabled, params, created_at)
			 VALUES (?, 'xray', 1, '{}', 1000)`, nodeID)
		if err != nil {
			return err
		}
		serviceID, _ := r.LastInsertId()

		r, err = tx.ExecContext(ctx,
			`INSERT INTO subjects (name, enabled, created_at) VALUES ('abuser', 1, 1000)`)
		if err != nil {
			return err
		}
		subjectID, _ = r.LastInsertId()

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO subject_services (subject_id, service_id) VALUES (?, ?)`,
			subjectID, serviceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO subject_credentials (subject_id, kind, value_enc, created_at)
			 VALUES (?, 'uuid', ?, 1000)`, subjectID, []byte("sealed")); err != nil {
			return err
		}

		// Exactly what subjects.Store.Freeze does.
		_, err = tx.ExecContext(ctx,
			`UPDATE subjects SET frozen_at = 2000, frozen_reason = 'abuse' WHERE id = ?`,
			subjectID)
		return err
	})
	if err != nil {
		t.Fatalf("seed frozen subject: %v", err)
	}
	return nodeID, subjectID
}

func TestAFrozenSubjectIsNotInTheDesiredDocument(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	nodeID, subjectID := frozenSubjectOnNode(t, st)

	var ids []int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		subs, err := buildSubjects(ctx, tx, nodeID, plainUnsealer{})
		if err != nil {
			return err
		}
		for _, s := range subs {
			ids = append(ids, s.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("buildSubjects: %v", err)
	}

	for _, id := range ids {
		if id == subjectID {
			t.Errorf("subject %d is frozen and still in the desired document for "+
				"node %d: the operator revoked them in the panel and the node was "+
				"handed a fresh revision that still serves them", subjectID, nodeID)
		}
	}
}

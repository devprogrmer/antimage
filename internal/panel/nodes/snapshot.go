package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/amyrm/antimage/internal/shared/canonical"
)

// nowUnix is the clock the expiry predicate reads. It is a variable so a test
// can freeze it and assert that an expired subject is omitted.
var nowUnix = func() int64 { return time.Now().UTC().Unix() }

// sortServices orders services by ID so the canonical document is byte-identical
// across builds. This does not rely on SQL row order: services.id aliases
// SQLite's rowid today, so a bare SELECT happens to return sorted rows, but
// that is incidental and SQLite does not guarantee scan order without ORDER BY.
func sortServices(services []Service) {
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })
}

// sortOutbounds orders egress paths by id, for the same reason services are:
// canonical serialization is only deterministic if the slice order is, and a
// non-deterministic document hashes differently on every build, so the node
// never reaches convergence.
func sortOutbounds(outbounds []Outbound) {
	sort.Slice(outbounds, func(i, j int) bool { return outbounds[i].ID < outbounds[j].ID })
}

// sortRoutingRules orders by priority, then by id.
//
// Priority is the evaluation order the operator chose, so it has to lead. The
// id tiebreak is not cosmetic: two rules sharing a priority would otherwise
// order however the query happened to return them, which makes the document
// hash unstable AND makes rule evaluation order differ between builds. Ties are
// legal, so the tiebreak has to be total.
func sortRoutingRules(rules []RoutingRule) {
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority < rules[j].Priority
		}
		return rules[i].ID < rules[j].ID
	})
}

// sortBalancers orders by id, the same reasoning sortOutbounds documents.
func sortBalancers(balancers []Balancer) {
	sort.Slice(balancers, func(i, j int) bool { return balancers[i].ID < balancers[j].ID })
}

// BuildDesiredSnapshot is the one authoritative reader of desired state
// (invariant 5).
//
// It takes a transaction rather than opening its own, which is what closes
// the read race in spec section 5: the revision counter and the rows that
// make up the document are read from a single consistent snapshot, so a
// document can never be labelled with a revision that does not describe it.
// Unsealer opens credential material sealed under the master key.
// *secrets.Box satisfies it; the interface keeps this package free of a
// dependency on the secrets package.
type Unsealer interface {
	Open(sealed []byte) ([]byte, error)
}

type snapshotOptions struct {
	unsealer Unsealer
}

// SnapshotOption configures BuildDesiredSnapshot.
type SnapshotOption func(*snapshotOptions)

// WithUnsealer supplies the key material needed to include subjects.
//
// Without it a node that HAS subjects fails to build rather than building a
// document that omits them: silently dropping subjects would deprovision every
// user on that node on the next convergence, which is a far worse outcome than
// refusing to issue a revision.
func WithUnsealer(u Unsealer) SnapshotOption {
	return func(o *snapshotOptions) { o.unsealer = u }
}

func BuildDesiredSnapshot(
	ctx context.Context, tx *sql.Tx, nodeID int64, opts ...SnapshotOption,
) (*Snapshot, error) {
	var options snapshotOptions
	for _, opt := range opts {
		opt(&options)
	}
	var revision int64
	err := tx.QueryRowContext(ctx,
		`SELECT desired_revision FROM nodes WHERE id = ?`, nodeID).Scan(&revision)
	if err != nil {
		return nil, fmt.Errorf("read revision for node %d: %w", nodeID, err)
	}

	// ORDER BY id gives the stable array ordering invariant 3 requires.
	rows, err := tx.QueryContext(ctx,
		`SELECT id, adapter_kind, enabled, params
		   FROM services WHERE node_id = ? ORDER BY id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read services for node %d: %w", nodeID, err)
	}
	defer func() { _ = rows.Close() }()

	var services []Service
	for rows.Next() {
		var (
			svc     Service
			enabled int
			params  string
		)
		if err := rows.Scan(&svc.ID, &svc.Kind, &enabled, &params); err != nil {
			return nil, fmt.Errorf("scan service: %w", err)
		}
		svc.Enabled = enabled == 1
		svc.Params = json.RawMessage(params)
		services = append(services, svc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate services: %w", err)
	}
	sortServices(services)

	subjects, err := buildSubjects(ctx, tx, nodeID, options.unsealer)
	if err != nil {
		return nil, err
	}

	outbounds, err := buildOutbounds(ctx, tx, nodeID, options.unsealer)
	if err != nil {
		return nil, err
	}
	routing, err := buildRouting(ctx, tx, nodeID)
	if err != nil {
		return nil, err
	}
	dns, err := buildDNS(ctx, tx, nodeID)
	if err != nil {
		return nil, err
	}

	doc := Document{
		Revision:  revision,
		NodeID:    nodeID,
		Services:  services,
		Subjects:  subjects,
		Outbounds: outbounds,
		Routing:   routing,
		DNS:       dns,
	}
	sortOutbounds(doc.Outbounds)
	if doc.Routing != nil {
		sortRoutingRules(doc.Routing.Rules)
		sortBalancers(doc.Routing.Balancers)
	}
	// Derived from content, not from the panel's maximum: a node given no
	// egress state keeps declaring v2 and its hash does not move.
	doc.SchemaVersion = effectiveSchemaVersion(doc)

	bytes, sum, err := canonical.Hash(doc)
	if err != nil {
		return nil, fmt.Errorf("canonicalize document for node %d: %w", nodeID, err)
	}
	return &Snapshot{Revision: revision, Document: doc, Bytes: bytes, SHA256: sum}, nil
}

// buildSubjects assembles the subjects entitled to service on this node.
//
// A subject appears exactly once, carrying every credential kind it holds,
// and only if it is enabled, unfrozen and unexpired. Expiry is enforced here
// rather than in generated protocol config: an expired subject simply stops
// being part of desired state, so the ordinary convergence path removes them
// and the removal is hash-verified like any other change. See the SP2 decision
// record, decision 2.
//
// frozen_at is checked here for the same reason, and it was missing. Freezing
// is documented as a revocation on the admin path (service.Subjects.SetFrozen)
// and is how quota exhaustion cuts a subject off, but subjects.Store.Freeze
// writes frozen_at without touching enabled -- so a frozen subject was rebuilt
// straight back into the document. SetFrozen republishes correctly, which made
// it worse rather than better: the node was promptly handed a NEW revision
// that still served the user the operator had just revoked.
//
// Ordering is by subject id and then credential kind so the canonical
// document is byte-identical across builds; invariant 3 depends on it.
func buildSubjects(
	ctx context.Context, tx *sql.Tx, nodeID int64, unsealer Unsealer,
) ([]Subject, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT s.id
		   FROM subjects s
		   JOIN subject_services ss ON ss.subject_id = s.id
		   JOIN services sv         ON sv.id = ss.service_id
		  WHERE sv.node_id = ?
		    AND s.enabled = 1
		    AND s.frozen_at IS NULL
		    AND (s.expires_at IS NULL OR s.expires_at > ?)
		  ORDER BY s.id`, nodeID, nowUnix())
	if err != nil {
		return nil, fmt.Errorf("read subjects for node %d: %w", nodeID, err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan subject id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate subjects: %w", err)
	}
	if len(ids) == 0 {
		// Explicit null, not an empty array: the document shape for a node
		// with no subjects must be byte-identical to what SP1 produced, or
		// every existing node's hash changes.
		return nil, nil
	}
	if unsealer == nil {
		return nil, fmt.Errorf(
			"node %d has %d subject(s) but no unsealer was supplied: "+
				"refusing to build a document that would deprovision them", nodeID, len(ids))
	}

	subjects := make([]Subject, 0, len(ids))
	for _, id := range ids {
		creds, err := subjectCredentials(ctx, tx, id, unsealer)
		if err != nil {
			return nil, err
		}

		// Fetch enforcement policies (schema v2)
		var maxDevices, maxIPs, maxConns, speedUp, speedDown sql.NullInt64
		err = tx.QueryRowContext(ctx,
			`SELECT max_devices, max_ips, max_connections,
			        speed_limit_up_kbps, speed_limit_down_kbps
			 FROM subjects WHERE id = ?`, id).
			Scan(&maxDevices, &maxIPs, &maxConns, &speedUp, &speedDown)
		if err != nil {
			return nil, fmt.Errorf("read enforcement policies for subject %d: %w", id, err)
		}

		subj := Subject{ID: id, Credentials: creds}
		if maxDevices.Valid {
			subj.MaxDevices = &maxDevices.Int64
		}
		if maxIPs.Valid {
			subj.MaxIPs = &maxIPs.Int64
		}
		if maxConns.Valid {
			subj.MaxConnections = &maxConns.Int64
		}
		if speedUp.Valid {
			subj.SpeedLimitUpKbps = &speedUp.Int64
		}
		if speedDown.Valid {
			subj.SpeedLimitDownKbps = &speedDown.Int64
		}

		subjects = append(subjects, subj)
	}
	return subjects, nil
}

func subjectCredentials(
	ctx context.Context, tx *sql.Tx, subjectID int64, unsealer Unsealer,
) ([]Credential, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT kind, value_enc FROM subject_credentials
		  WHERE subject_id = ? ORDER BY kind`, subjectID)
	if err != nil {
		return nil, fmt.Errorf("read credentials for subject %d: %w", subjectID, err)
	}
	defer func() { _ = rows.Close() }()

	var creds []Credential
	for rows.Next() {
		var (
			kind   string
			sealed []byte
		)
		if err := rows.Scan(&kind, &sealed); err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}
		plain, err := unsealer.Open(sealed)
		if err != nil {
			// Wrong master key, or a corrupted row. Either way the document
			// must not be issued with a credential the node cannot use.
			return nil, fmt.Errorf(
				"unseal %s credential for subject %d (wrong master key?): %w", kind, subjectID, err)
		}
		creds = append(creds, Credential{Kind: kind, Value: string(plain)})
	}
	return creds, rows.Err()
}

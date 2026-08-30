package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestPremiumLayerSchema(t *testing.T) {
	s := openTemp(t)

	t.Run("service_templates table exists with constraints", func(t *testing.T) {
		var count int
		err := s.Read().QueryRow(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE type='table' AND name='service_templates'
		`).Scan(&count)
		if err != nil {
			t.Fatalf("query table existence: %v", err)
		}
		if count != 1 {
			t.Errorf("service_templates table does not exist")
		}

		// adapter_kind is deliberately unconstrained since 00038.
		//
		// It used to carry CHECK (adapter_kind IN ('xray','singbox','openvpn',
		// 'l2tp')), which was wrong in both directions: it permitted openvpn,
		// for which no adapter has ever existed, and rejected wireguard and
		// hysteria2, which ship. So this asserted that the database enforced a
		// list of protocols that did not match the ones the product could run.
		//
		// Which kinds are real is the adapters' knowledge, published through
		// adapter.Caps, and a copy of it in SQL cannot be kept current. The
		// test now checks the property that actually matters: a template for a
		// shipped adapter can be saved.
		for _, kind := range []string{"wireguard", "hysteria2", "ocserv", "xray"} {
			err = s.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`
					INSERT INTO service_templates
					(name, adapter_kind, params_json, created_at, updated_at)
					VALUES (?, ?, '{}', 1, 1)
				`, "tpl-"+kind, kind)
				return err
			})
			if err != nil {
				t.Errorf("a template for %s -- a shipped adapter -- was refused by "+
					"the database: %v", kind, err)
			}
		}
	})

	t.Run("user_presets table exists", func(t *testing.T) {
		var count int
		err := s.Read().QueryRow(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE type='table' AND name='user_presets'
		`).Scan(&count)
		if err != nil {
			t.Fatalf("query table existence: %v", err)
		}
		if count != 1 {
			t.Errorf("user_presets table does not exist")
		}
	})

	t.Run("bulk_operations table exists with status constraint", func(t *testing.T) {
		var count int
		err := s.Read().QueryRow(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE type='table' AND name='bulk_operations'
		`).Scan(&count)
		if err != nil {
			t.Fatalf("query table existence: %v", err)
		}
		if count != 1 {
			t.Errorf("bulk_operations table does not exist")
		}

		// Test operation_type constraint
		err = s.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO bulk_operations
				(operation_type, total_items, created_at)
				VALUES ('invalid_op', 5, 1)
			`)
			return err
		})
		if err == nil {
			t.Error("expected operation_type constraint violation, got nil")
		}
	})

	t.Run("dashboard_stats table exists", func(t *testing.T) {
		var count int
		err := s.Read().QueryRow(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE type='table' AND name='dashboard_stats'
		`).Scan(&count)
		if err != nil {
			t.Fatalf("query table existence: %v", err)
		}
		if count != 1 {
			t.Errorf("dashboard_stats table does not exist")
		}
	})

	t.Run("nodes.tags_json column exists", func(t *testing.T) {
		var count int
		err := s.Read().QueryRow(`
			SELECT COUNT(*) FROM pragma_table_info('nodes')
			WHERE name='tags_json'
		`).Scan(&count)
		if err != nil {
			t.Fatalf("query column existence: %v", err)
		}
		if count != 1 {
			t.Errorf("nodes.tags_json column does not exist")
		}
	})

	t.Run("indexes exist", func(t *testing.T) {
		indexes := []string{
			"service_templates_adapter",
			"service_templates_creator",
			"user_presets_creator",
			"bulk_operations_actor",
			"bulk_operations_status",
			"dashboard_stats_computed",
			"nodes_tags",
		}
		for _, idx := range indexes {
			var count int
			err := s.Read().QueryRow(`
				SELECT COUNT(*) FROM sqlite_master
				WHERE type='index' AND name=?
			`, idx).Scan(&count)
			if err != nil {
				t.Fatalf("query index %s: %v", idx, err)
			}
			if count != 1 {
				t.Errorf("index %s does not exist", idx)
			}
		}
	})
}

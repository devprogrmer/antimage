package deployment

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/amyrm/antimage/internal/panel/store"
)

func TestValidatorPortConflicts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// Create test node
	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (id, name, address, status, created_at)
			 VALUES (1, 'test-node', '10.0.0.1:8443', 'online', 1000000)`)
		return err
	})
	if err != nil {
		t.Fatalf("setup node: %v", err)
	}

	validator := NewValidator(st)

	t.Run("detects port conflict on same node", func(t *testing.T) {
		desiredState := map[string]interface{}{
			"nodes": map[string]interface{}{
				"1": map[string]interface{}{
					"services": []interface{}{
						map[string]interface{}{
							"protocol": "vless",
							"port":     float64(443),
							"listen":   "0.0.0.0",
						},
						map[string]interface{}{
							"protocol": "vmess",
							"port":     float64(443), // conflict
							"listen":   "0.0.0.0",
						},
					},
				},
			},
		}

		result, err := validator.ValidateDesiredState(ctx, desiredState)
		if err != nil {
			t.Fatalf("validate: %v", err)
		}

		if result.Valid {
			t.Error("expected validation to fail on port conflict")
		}

		foundConflict := false
		for _, conflict := range result.Conflicts {
			if conflict.Type == "port_conflict" {
				foundConflict = true
				break
			}
		}
		if !foundConflict {
			t.Error("expected port_conflict in conflicts list")
		}
	})

	t.Run("accepts different ports", func(t *testing.T) {
		desiredState := map[string]interface{}{
			"nodes": map[string]interface{}{
				"1": map[string]interface{}{
					"services": []interface{}{
						map[string]interface{}{
							"protocol": "vless",
							"port":     float64(443),
							"listen":   "0.0.0.0",
						},
						map[string]interface{}{
							"protocol": "vmess",
							"port":     float64(8443),
							"listen":   "0.0.0.0",
						},
					},
				},
			},
		}

		result, err := validator.ValidateDesiredState(ctx, desiredState)
		if err != nil {
			t.Fatalf("validate: %v", err)
		}

		if !result.Valid {
			t.Errorf("expected validation to pass, got conflicts: %+v", result.Conflicts)
		}
	})
}

func TestValidatorProtocolValidation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// Create test node
	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (id, name, address, status, created_at)
			 VALUES (1, 'test-node', '10.0.0.1:8443', 'online', 1000000)`)
		return err
	})
	if err != nil {
		t.Fatalf("setup node: %v", err)
	}

	validator := NewValidator(st)

	t.Run("rejects invalid port range", func(t *testing.T) {
		desiredState := map[string]interface{}{
			"nodes": map[string]interface{}{
				"1": map[string]interface{}{
					"services": []interface{}{
						map[string]interface{}{
							"protocol": "vless",
							"port":     float64(70000), // invalid
							"listen":   "0.0.0.0",
						},
					},
				},
			},
		}

		result, err := validator.ValidateDesiredState(ctx, desiredState)
		if err != nil {
			t.Fatalf("validate: %v", err)
		}

		if result.Valid {
			t.Error("expected validation to fail on invalid port")
		}

		foundInvalidPort := false
		for _, conflict := range result.Conflicts {
			if conflict.Type == "invalid_port" {
				foundInvalidPort = true
				break
			}
		}
		if !foundInvalidPort {
			t.Error("expected invalid_port in conflicts list")
		}
	})

	t.Run("rejects invalid listen IP", func(t *testing.T) {
		desiredState := map[string]interface{}{
			"nodes": map[string]interface{}{
				"1": map[string]interface{}{
					"services": []interface{}{
						map[string]interface{}{
							"protocol": "vless",
							"port":     float64(443),
							"listen":   "999.999.999.999", // invalid
						},
					},
				},
			},
		}

		result, err := validator.ValidateDesiredState(ctx, desiredState)
		if err != nil {
			t.Fatalf("validate: %v", err)
		}

		if result.Valid {
			t.Error("expected validation to fail on invalid listen IP")
		}

		foundInvalidIP := false
		for _, conflict := range result.Conflicts {
			if conflict.Type == "invalid_listen_ip" {
				foundInvalidIP = true
				break
			}
		}
		if !foundInvalidIP {
			t.Error("expected invalid_listen_ip in conflicts list")
		}
	})

	t.Run("warns on unknown protocol", func(t *testing.T) {
		desiredState := map[string]interface{}{
			"nodes": map[string]interface{}{
				"1": map[string]interface{}{
					"services": []interface{}{
						map[string]interface{}{
							"protocol": "unknown-protocol",
							"port":     float64(443),
							"listen":   "0.0.0.0",
						},
					},
				},
			},
		}

		result, err := validator.ValidateDesiredState(ctx, desiredState)
		if err != nil {
			t.Fatalf("validate: %v", err)
		}

		if !result.Valid {
			t.Errorf("expected validation to pass with warning, got conflicts: %+v", result.Conflicts)
		}

		foundWarning := false
		for _, warning := range result.Warnings {
			if warning.Type == "unknown_protocol" {
				foundWarning = true
				break
			}
		}
		if !foundWarning {
			t.Error("expected unknown_protocol warning")
		}
	})
}

func TestValidatorNodeReferences(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// Create test node
	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (id, name, address, status, created_at)
			 VALUES (1, 'test-node', '10.0.0.1:8443', 'online', 1000000)`)
		return err
	})
	if err != nil {
		t.Fatalf("setup node: %v", err)
	}

	validator := NewValidator(st)

	t.Run("rejects reference to nonexistent node", func(t *testing.T) {
		desiredState := map[string]interface{}{
			"nodes": map[string]interface{}{
				"999": map[string]interface{}{ // node doesn't exist
					"services": []interface{}{
						map[string]interface{}{
							"protocol": "vless",
							"port":     float64(443),
						},
					},
				},
			},
		}

		result, err := validator.ValidateDesiredState(ctx, desiredState)
		if err != nil {
			t.Fatalf("validate: %v", err)
		}

		if result.Valid {
			t.Error("expected validation to fail on unknown node")
		}

		foundUnknownNode := false
		for _, conflict := range result.Conflicts {
			if conflict.Type == "unknown_node" {
				foundUnknownNode = true
				break
			}
		}
		if !foundUnknownNode {
			t.Error("expected unknown_node in conflicts list")
		}
	})
}

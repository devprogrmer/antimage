package devices

import (
	"testing"
)

func TestRegisterDevice(t *testing.T) {
	t.Run("register new device", func(t *testing.T) {
		t.Skip("requires test database setup")
	})

	t.Run("device limit enforcement", func(t *testing.T) {
		t.Skip("requires test database setup")
	})

	t.Run("revoked device rejection", func(t *testing.T) {
		t.Skip("requires test database setup")
	})
}

func TestCheckIPLimit(t *testing.T) {
	t.Run("unlimited IPs", func(t *testing.T) {
		t.Skip("requires test database setup")
	})

	t.Run("enforce IP limit", func(t *testing.T) {
		t.Skip("requires test database setup")
	})

	t.Run("same IP reconnection allowed", func(t *testing.T) {
		t.Skip("requires test database setup")
	})
}

func TestCheckConnectionLimit(t *testing.T) {
	t.Run("unlimited connections", func(t *testing.T) {
		t.Skip("requires test database setup")
	})

	t.Run("enforce connection limit", func(t *testing.T) {
		t.Skip("requires test database setup")
	})
}

package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

func grantWrite(t *testing.T, f *readFixture) {
	t.Helper()
	perms, err := json.Marshal([]rbac.Permission{
		rbac.PermSubjectRead, rbac.PermSubjectWrite, rbac.PermCredReveal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE roles SET permissions = ? WHERE id = 1`, string(perms))
		return err
	}); err != nil {
		t.Fatalf("grant write: %v", err)
	}
}

func TestDisableIsTenantScoped(t *testing.T) {
	f := newReadFixture(t)
	grantWrite(t, f)

	got := f.send(aliceChat, "/disable alice-customer")
	if !strings.Contains(got, "Disabled") {
		t.Fatalf("/disable own user: %q", got)
	}

	theirs := f.send(aliceChat, "/disable bob-customer")
	missing := f.send(aliceChat, "/disable no-such-name-at-all")
	if theirs != missing {
		t.Errorf("disable reply differs for foreign vs missing:\n%q\nvs\n%q", theirs, missing)
	}
	if strings.Contains(theirs, "bob") {
		t.Errorf("LEAK: disable mentioned the other tenant: %q", theirs)
	}
}

func TestCreateAsResellerIsOwned(t *testing.T) {
	f := newReadFixture(t)
	grantWrite(t, f)

	got := f.send(aliceChat, "/create new-alice 30 1")
	if !strings.Contains(got, "Created new-alice") {
		t.Fatalf("/create: %q", got)
	}
	listed := f.send(aliceChat, "/users")
	if !strings.Contains(listed, "new-alice") {
		t.Errorf("created user missing from /users:\n%s", listed)
	}
	bobs := f.send(bobChat, "/users")
	if strings.Contains(bobs, "new-alice") {
		t.Errorf("LEAK: bob sees alice's new user:\n%s", bobs)
	}
}

func TestCreateWithoutWriteIsForbidden(t *testing.T) {
	f := newReadFixture(t)
	got := f.send(aliceChat, "/create sneaky")
	if strings.Contains(got, "Created") {
		t.Fatalf("created without subject:write: %q", got)
	}
	if !strings.Contains(got, "role does not allow") {
		t.Errorf("unexpected refusal: %q", got)
	}
}

func TestExtendOwnUser(t *testing.T) {
	f := newReadFixture(t)
	grantWrite(t, f)
	got := f.send(aliceChat, "/extend alice-customer 7")
	if !strings.Contains(got, "Extended alice-customer") {
		t.Fatalf("/extend: %q", got)
	}
}

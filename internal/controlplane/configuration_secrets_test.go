package controlplane

import (
	"context"
	"testing"
)

func TestConfigurationSecretsRollbackTogetherOnDatabaseFailure(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	oldBusiness, oldTicket := "http://business-before.example.com", "http://ticket-before.example.com"
	if err := store.ReplaceSettingsAndSecrets(ctx, map[string]any{"generation": "before"}, map[string]*string{"business": &oldBusiness, "ticket": &oldTicket}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_ticket_secret BEFORE INSERT ON encrypted_secrets WHEN NEW.name = 'ticket' BEGIN SELECT RAISE(ABORT, 'simulated secret failure'); END`); err != nil {
		t.Fatal(err)
	}
	newBusiness, newTicket := "http://business-after.example.com", "http://ticket-after.example.com"
	if err := store.ReplaceSettingsAndSecrets(ctx, map[string]any{"generation": "after"}, map[string]*string{"business": &newBusiness, "ticket": &newTicket}); err == nil {
		t.Fatal("expected database failure")
	}
	settings, err := store.ReadSettings(ctx)
	if err != nil || settings["generation"] != "before" {
		t.Fatal("settings escaped failed transaction")
	}
	for name, want := range map[string]string{"business": oldBusiness, "ticket": oldTicket} {
		got, found, err := store.ReadSecret(ctx, name)
		if err != nil || !found || got != want {
			t.Fatal("secret escaped failed transaction")
		}
	}
}

package failover

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/gateway"
	"github.com/Alfonsxh/codex-cpa-pool/internal/reasoningpolicy"
)

func TestAuthSnapshotCarriesReasoningCeilingWithoutChangingIdentity(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	ctx := context.Background()
	publisher := &AuthSnapshotPublisher{Root: store.Root(), Store: store, Fence: &testWriteFence{}}
	path := filepath.Join(store.Root(), authSnapshotRelativePath)
	read := func() (gateway.AuthSnapshot, []byte) {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := gateway.ParseAuthSnapshot(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		return *snapshot, raw
	}
	if _, err := publisher.PublishAuthSnapshot(ctx, false); err != nil {
		t.Fatal(err)
	}
	before, _ := read()
	for _, limit := range []string{"xhigh", "high", "unlimited"} {
		if err := store.WriteSettings(ctx, map[string]any{reasoningpolicy.SettingKey: limit}); err != nil {
			t.Fatal(err)
		}
		if _, err := publisher.PublishAuthSnapshot(ctx, false); err != nil {
			t.Fatal(err)
		}
		after, _ := read()
		want := limit
		if want == "unlimited" {
			want = ""
		}
		if after.MaxReasoningEffort != want || !reflect.DeepEqual(before.Records, after.Records) {
			t.Fatal("policy publication lost the limit or changed identities")
		}
	}
	_, valid := read()
	if err := store.WriteSettings(ctx, map[string]any{reasoningpolicy.SettingKey: "invalid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.PublishAuthSnapshot(ctx, false); err == nil {
		t.Fatal("invalid stored policy was published")
	}
	_, after := read()
	if !bytes.Equal(valid, after) {
		t.Fatal("invalid policy replaced last valid snapshot")
	}
}

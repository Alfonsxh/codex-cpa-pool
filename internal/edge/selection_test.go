package edge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelectionParserAcceptsOnlyExistingBlueGreenDirective(t *testing.T) {
	for raw, expected := range map[string]Slot{
		"set $active_gateway_backend gateway-blue:8317;\n":                   Blue,
		"# managed\n\n set   $active_gateway_backend gateway-green:8317; \n": Green,
	} {
		slot, err := ParseSelection(strings.NewReader(raw))
		if err != nil || slot != expected {
			t.Fatalf("ParseSelection(%q) = (%q, %v), want %q", raw, slot, err, expected)
		}
	}
	for _, raw := range []string{
		"", "set $active_gateway_backend gateway-blue:8317;\nset $active_gateway_backend gateway-green:8317;\n",
		"set $active_gateway_backend attacker:8317;\n", "include /tmp/unsafe.conf;\n",
		"set $active_gateway_backend gateway-blue:8317;\n" + strings.Repeat("# padding\n", 600),
	} {
		if _, err := ParseSelection(strings.NewReader(raw)); err == nil {
			t.Fatalf("unsafe selection accepted: %q", raw)
		}
	}
}

func TestSelectorKeepsLastValidSlotAndRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "active-gateway.conf")
	writeSelectionFixture(t, path, Blue)
	selector, err := NewSelector(path, 10*time.Millisecond, nil)
	if err != nil || selector.Slot() != Blue {
		t.Fatalf("NewSelector = (%#v, %v)", selector, err)
	}
	writeSelectionFixture(t, path, Green)
	changed, err := selector.Refresh()
	if err != nil || !changed || selector.Slot() != Green {
		t.Fatalf("Refresh green = (%v, %v), slot=%q", changed, err, selector.Slot())
	}
	if err := os.WriteFile(path, []byte("include /tmp/unsafe;\n"), 0o644); err != nil {
		t.Fatalf("write unsafe selection: %v", err)
	}
	if _, err := selector.Refresh(); err == nil || selector.Slot() != Green {
		t.Fatalf("unsafe refresh changed slot: slot=%q err=%v", selector.Slot(), err)
	}

	external := filepath.Join(directory, "external.conf")
	writeSelectionFixture(t, external, Blue)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove selection: %v", err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Fatalf("symlink selection: %v", err)
	}
	if _, err := ReadSelection(path); err == nil {
		t.Fatal("symlink selection was accepted")
	}
}

func TestSelectorWatchesAtomicSelectionReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "active-gateway.conf")
	writeSelectionFixture(t, path, Blue)
	selector, err := NewSelector(path, 10*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("NewSelector: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- selector.Run(ctx) }()
	temporary := filepath.Join(directory, ".active-gateway.tmp")
	writeSelectionFixture(t, temporary, Green)
	if err := os.Rename(temporary, path); err != nil {
		cancel()
		t.Fatalf("replace selection: %v", err)
	}
	// The selector refreshes from a timer as well as fsnotify events, but a
	// heavily loaded machine can starve the run goroutine past a short deadline.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && selector.Slot() != Green {
		time.Sleep(5 * time.Millisecond)
	}
	if selector.Slot() != Green {
		cancel()
		t.Fatalf("watched slot = %q, want green", selector.Slot())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("selector Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("selector watcher did not stop")
	}
}

func writeSelectionFixture(t *testing.T, path string, slot Slot) {
	t.Helper()
	raw := "set $active_gateway_backend gateway-" + string(slot) + ":8317;\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write selection fixture: %v", err)
	}
}

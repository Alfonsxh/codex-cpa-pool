package admin

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/runtimeops"
)

func TestAccountCreationQueuesTicketAndSeparatesInstallationFailure(t *testing.T) {
	for _, mode := range []string{"queued", "queue-failed", "creation-failed"} {
		t.Run(mode, func(t *testing.T) {
			base, store := newTestAdmin(t)
			base.Close()
			service := &fakeAccountLifecycle{}
			jobs := &fakeRuntimeJobs{submission: runtimeops.JobSubmission{Job: runtimeops.Job{ID: "install-new-account", Status: "queued"}}}
			if mode == "queue-failed" {
				jobs.submitError = errors.New("internal-private-error")
			}
			if mode == "creation-failed" {
				service.err = errors.New("creation failed")
			}
			server, err := New(Config{Store: store, AccountLifecycle: service, RuntimeJobs: jobs, Extensions: &runtimeops.Extensions{}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(server.Close)
			response := performAdminRequest(server, http.MethodPost, "/admin/api/accounts", map[string]any{"id": "gamma", "email": "gamma@accounts.example.com", "proxy_mode": "direct"}, map[string]string{"X-Management-Key": "test-management-key"}, nil)
			if mode == "creation-failed" {
				if jobs.submitCalls != 0 {
					t.Fatal("installation queued after failed creation")
				}
				return
			}
			if response.Code != http.StatusCreated || jobs.submitCalls != 1 || jobs.action != "plugin-update" || jobs.target != "gamma" {
				t.Fatalf("creation/install result: %d %s %+v", response.Code, response.Body.String(), jobs)
			}
			if mode == "queued" && !strings.Contains(response.Body.String(), "install-new-account") {
				t.Fatal("job not returned")
			}
			if mode == "queue-failed" && (!strings.Contains(response.Body.String(), "submission_failed") || strings.Contains(response.Body.String(), "internal-private-error")) {
				t.Fatal("plugin failure lost or leaked")
			}
		})
	}
}

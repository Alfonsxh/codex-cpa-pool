package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestPlanEmitsEveryComponentOnceInStableOrder(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	var output bytes.Buffer
	if err := runManifest([]string{"plan", "--root", root}, &output); err != nil {
		t.Fatalf("runManifest plan: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != len(releaseComponents) {
		t.Fatalf("plan lines = %d, want %d: %q", len(lines), len(releaseComponents), lines)
	}
	for index, component := range releaseComponents {
		fields := strings.Split(lines[index], "\t")
		if len(fields) != 2 || fields[0] != component || !validSHA256(fields[1]) {
			t.Fatalf("plan line %d = %q", index, lines[index])
		}
	}
}

func TestImageMetadataReadsBuildxLabelsWithoutPullingLayers(t *testing.T) {
	digest := strings.Repeat("a", 64)
	input := filepath.Join(t.TempDir(), "metadata.json")
	payload := `{
  "name": "ghcr.io/example/codex-cpa-web:v2.0.0",
  "manifest": {"digest": "sha256:` + strings.Repeat("b", 64) + `"},
  "image": {"config": {"Labels": {
    "io.codex-cpa.component": "web",
    "io.codex-cpa.component-digest": "` + digest + `",
    "io.codex-cpa.source-digest": "` + digest + `"
  }}}
}`
	if err := os.WriteFile(input, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runImageMetadata([]string{"--input", input}, &output); err != nil {
		t.Fatalf("runImageMetadata: %v", err)
	}
	want := "sha256:" + strings.Repeat("b", 64) + "\tweb\t" + digest + "\t" + digest + "\n"
	if output.String() != want {
		t.Fatalf("metadata = %q, want %q", output.String(), want)
	}
}

func TestImageMetadataRejectsMissingReleaseLabels(t *testing.T) {
	input := filepath.Join(t.TempDir(), "metadata.json")
	payload := `{"manifest":{"digest":"sha256:` + strings.Repeat("b", 64) + `"},"image":{"config":{"Labels":{}}}}`
	if err := os.WriteFile(input, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runImageMetadata([]string{"--input", input}, &bytes.Buffer{}); err == nil {
		t.Fatal("runImageMetadata accepted missing release labels")
	}
}

func TestChecksumWritesAtomicPortableLines(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.txt")
	second := filepath.Join(root, "second.txt")
	output := filepath.Join(root, "SHA256SUMS")
	if err := os.WriteFile(first, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runChecksum([]string{"--output", output, first, second}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runChecksum: %v", err)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "  first.txt") || !strings.HasSuffix(lines[1], "  second.txt") {
		t.Fatalf("checksum lines = %q", lines)
	}
}

func TestWriteDeployEnvironmentProducesStrictMachineReadableContract(t *testing.T) {
	output := filepath.Join(t.TempDir(), "release.env")
	descriptor := releaseDescriptor{
		SchemaVersion:  1,
		ReleaseVersion: "v2.0.0",
		Revision:       strings.Repeat("a", 40),
		ArchiveName:    "codex-cpa-pool-v2.0.0.tar.gz",
		Components: map[string]descriptorRecord{
			"control": {Image: "ghcr.io/example/codex-cpa-control:sha256-" + strings.Repeat("a", 64)},
			"web":     {Image: "ghcr.io/example/codex-cpa-web:sha256-" + strings.Repeat("b", 64)},
			"gateway": {Image: "ghcr.io/example/codex-cpa-gateway:sha256-" + strings.Repeat("c", 64)},
			"edge":    {Image: "ghcr.io/example/codex-cpa-edge:sha256-" + strings.Repeat("d", 64)},
		},
	}
	if err := writeDeployEnvironment(output, descriptor); err != nil {
		t.Fatalf("writeDeployEnvironment: %v", err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	for _, expected := range []string{
		"CPAP_RELEASE_VERSION=v2.0.0\n",
		"CPAP_RELEASE_ARCHIVE=codex-cpa-pool-v2.0.0.tar.gz\n",
		"CPAP_CONTROL_IMAGE=ghcr.io/example/codex-cpa-control:sha256-",
		"CPAP_EDGE_IMAGE=ghcr.io/example/codex-cpa-edge:sha256-",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("deploy environment missing %q:\n%s", expected, content)
		}
	}
	if strings.ContainsAny(content, "'\"") {
		t.Fatalf("deploy environment unexpectedly requires shell quoting:\n%s", content)
	}
}

func TestArchiveVerificationRejectsTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "unsafe.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	writer := tar.NewWriter(compressed)
	if err := writer.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o600, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := verifyArchive(archive); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("verifyArchive error = %v", err)
	}
}

func TestArchiveVerificationRejectsRemovedRuntimePaths(t *testing.T) {
	for _, path := range []string{
		"scripts/worker.py",
		"gateway/request_gate.lua",
		"requirements-test.txt",
		"admin/index.html",
		"cmd/docker-read-" + "proxy/main.go",
		"internal/dockerread" + "proxy/server.go",
		"release/Docker" + "file",
		"testdata/v" + "2/fixture.json",
		"v" + "2/Dockerfile",
	} {
		t.Run(path, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "removed-runtime.tar.gz")
			file, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			compressed := gzip.NewWriter(file)
			writer := tar.NewWriter(compressed)
			if err := writer.WriteHeader(&tar.Header{Name: path, Mode: 0o600, Size: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := compressed.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if err := verifyArchive(archive); err == nil || !strings.Contains(err.Error(), "removed runtime path") {
				t.Fatalf("verifyArchive error = %v", err)
			}
		})
	}
}

func TestArchiveVerificationRejectsGeneratedArtifacts(t *testing.T) {
	for _, path := range []string{
		"frontend/dist/admin/index.html",
		"frontend/node_modules/package/index.js",
		"frontend/coverage/index.html",
		"frontend/playwright-report/index.html",
		"frontend/test-results/.last-run.json",
	} {
		t.Run(path, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "generated-artifact.tar.gz")
			file, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			compressed := gzip.NewWriter(file)
			writer := tar.NewWriter(compressed)
			if err := writer.WriteHeader(&tar.Header{Name: path, Mode: 0o600, Size: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := compressed.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if err := verifyArchive(archive); err == nil || !strings.Contains(err.Error(), "generated test or build artifact") {
				t.Fatalf("verifyArchive error = %v", err)
			}
		})
	}
}

func TestWebhookScannerAllowsOnlyExplicitFixtureKeys(t *testing.T) {
	if containsWebhookSecret("https://example.com/cgi-bin/webhook/send?key=test-placeholder") {
		t.Fatal("fixture webhook key was rejected")
	}
	secretURL := "https://example.com/cgi-bin/webhook/send?key=" + "real-looking-key"
	if !containsWebhookSecret(secretURL) {
		t.Fatal("non-fixture webhook key was accepted")
	}
}

func TestPublicCommunityDomainDoesNotAllowLookalikes(t *testing.T) {
	for _, test := range []struct {
		host string
		want bool
	}{
		{host: "t.me", want: true},
		{host: "T.ME", want: true},
		{host: "api.telegram.org", want: true},
		{host: "api.telegram.org.attacker.net", want: false},
		{host: "private.api.telegram.org", want: false},
		{host: "t.me.attacker.net", want: false},
		{host: "private.t.me", want: false},
		{host: "not-t.me", want: false},
	} {
		t.Run(test.host, func(t *testing.T) {
			if got := allowedDomain(test.host); got != test.want {
				t.Fatalf("allowedDomain(%q) = %v, want %v", test.host, got, test.want)
			}
		})
	}
}

func TestControlMessageChangesInvalidateOnlyTheControlImage(t *testing.T) {
	root := t.TempDir()
	for _, inputs := range componentInputs {
		for _, input := range inputs {
			if err := os.MkdirAll(filepath.Join(root, input), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, relative := range []string{"internal/i18n/catalog/zh-CN.json", "internal/httpi18n/locale.go"} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
		before, err := buildManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("updated"), 0o600); err != nil {
			t.Fatal(err)
		}
		after, err := buildManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, component := range releaseComponents {
			changed := before.Components[component].SourceSHA256 != after.Components[component].SourceSHA256
			if changed != (component == "control") {
				t.Fatalf("%s: %s digest changed = %v", relative, component, changed)
			}
		}
	}
}

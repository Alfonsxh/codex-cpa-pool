package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	manifestVersion          = 1
	maxReleaseText           = 4 << 20
	releaseDescriptorVersion = 1
)

type componentRecord struct {
	SourceSHA256 string   `json:"source_sha256"`
	Inputs       []string `json:"inputs"`
}

type releaseManifest struct {
	Version    int                        `json:"version"`
	Components map[string]componentRecord `json:"components"`
}

type buildxImageMetadata struct {
	Name     string `json:"name"`
	Manifest struct {
		Digest string `json:"digest"`
	} `json:"manifest"`
	Image struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	} `json:"image"`
}

type releaseDescriptor struct {
	SchemaVersion  int                         `json:"schema_version"`
	ReleaseVersion string                      `json:"release_version"`
	Revision       string                      `json:"revision"`
	ImagePrefix    string                      `json:"image_prefix"`
	ArchiveName    string                      `json:"archive_name"`
	Components     map[string]descriptorRecord `json:"components"`
}

type descriptorRecord struct {
	Image        string `json:"image"`
	SourceSHA256 string `json:"source_sha256"`
}

var componentInputs = map[string][]string{
	"control": {
		".dockerignore", "Dockerfile", "docker-bake.hcl", "go.mod", "go.sum",
		"cmd/admin", "cmd/bootstrap", "cmd/collector", "cmd/failover",
		"cmd/log-maintenance", "cmd/notifications", "cmd/ownership", "cmd/quota",
		"cmd/releasectl",
		"internal/cpaplugin", "plugins/codex-ticket", "scripts/build-codex-plugin.sh",
		"internal/accountconfig", "internal/accountlifecycle", "internal/accountprojection", "internal/accountstatus",
		"internal/admin", "internal/branding", "internal/collector", "internal/contract",
		"internal/bootstrap", "internal/controlplane", "internal/failover",
		"internal/gateway", "internal/httpi18n", "internal/i18n", "internal/identity", "internal/logmaintenance", "internal/notifications", "internal/reasoningpolicy",
		"internal/ownership", "internal/portal", "internal/quota", "internal/runtimeops",
		"internal/scheduler", "internal/sitetime", "internal/snapshotfile", "internal/usage", "internal/usagereport",
	},
	"web": {
		".dockerignore", "Dockerfile", "docker-bake.hcl", "go.mod", "go.sum", "cmd/web", "internal/web",
		"frontend/README.md", "frontend/index.html", "frontend/package.json",
		"frontend/package-lock.json", "frontend/portal", "frontend/usage", "frontend/scripts",
		"frontend/src", "frontend/tsconfig.json", "frontend/vite.config.ts",
		"frontend/vite.portal.config.ts", "frontend/vite.shared.ts", "frontend/vite.usage.config.ts",
	},
	"gateway": {
		".dockerignore", "Dockerfile", "docker-bake.hcl", "go.mod", "go.sum", "cmd/gateway", "internal/gateway", "internal/reasoningpolicy",
	},
	"edge": {
		".dockerignore", "Dockerfile", "docker-bake.hcl", "go.mod", "go.sum", "cmd/edge", "internal/edge",
	},
}

var releaseComponents = []string{"control", "web", "gateway", "edge"}

var (
	semanticVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-.][0-9A-Za-z.-]+)?$`)
	registryPrefixPattern  = regexp.MustCompile(`^[A-Za-z0-9.-]+(?::[0-9]+)?/[A-Za-z0-9._/-]+$`)
	emailPattern           = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@([A-Za-z0-9.-]+\.[A-Za-z]{2,63})`)
	ipv4Pattern            = regexp.MustCompile(`(?:^|[^0-9.])((?:[0-9]{1,3}\.){3}[0-9]{1,3})(?:$|[^0-9.])`)
	urlPattern             = regexp.MustCompile(`https?://(?:[^/@\s]+@)?((?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,63})`)
	registryHostPattern    = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])((?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,63})/[A-Za-z0-9._/-]+[:@]`)
	webhookSecretPattern   = regexp.MustCompile(`(?i)https?://[^\s'\"]+/cgi-bin/webhook/send\?key=([A-Za-z0-9_-]+)`)
)

var allowedDomainSuffixes = []string{
	"alpinelinux.org", "aliyun.com", "chatgpt.com", "daocloud.io", "debian.org",
	"docker.io", "dotenvx.com", "example.com", "example.net", "example.org",
	"github.com", "githubusercontent.com", "ghcr.io", "golang.google.cn", "goproxy.cn", "invalid",
	"mcr.microsoft.com",
	"npmmirror.com", "openai.com", "opencollective.com", "paulmillr.com",
	"shields.io", "test", "tidelift.com", "w3.org", "weixin.qq.com",
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("usage: cpa-releasectl <manifest|image-metadata|privacy|archive|checksum> ...")
	}
	switch arguments[0] {
	case "manifest":
		return runManifest(arguments[1:], output)
	case "image-metadata":
		return runImageMetadata(arguments[1:], output)
	case "privacy":
		return runPrivacy(arguments[1:], output)
	case "archive":
		return runArchive(arguments[1:], output)
	case "checksum":
		return runChecksum(arguments[1:], output)
	default:
		return fmt.Errorf("unsupported release command: %s", arguments[0])
	}
}

func runManifest(arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("usage: cpa-releasectl manifest <digest|plan|create|verify|get|descriptor|deploy-env>")
	}
	flags := flag.NewFlagSet("manifest "+arguments[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", ".", "repository root")
	manifestPath := flags.String("manifest", "", "manifest path")
	component := flags.String("component", "", "component name")
	outputPath := flags.String("output", "", "output path")
	releaseVersion := flags.String("release-version", "", "release version")
	revision := flags.String("revision", "", "Git revision")
	imagePrefix := flags.String("image-prefix", "", "image prefix")
	archiveName := flags.String("archive-name", "", "archive base name")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	switch arguments[0] {
	case "digest":
		inputs, found := componentInputs[strings.TrimSpace(*component)]
		if !found {
			return fmt.Errorf("unknown release component: %s", *component)
		}
		digest, err := componentDigest(absRoot, inputs)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, digest)
		return err
	case "plan":
		manifest, err := buildManifest(absRoot)
		if err != nil {
			return err
		}
		for _, componentName := range releaseComponents {
			if _, err := fmt.Fprintf(output, "%s\t%s\n", componentName, manifest.Components[componentName].SourceSHA256); err != nil {
				return err
			}
		}
		return nil
	case "create":
		if strings.TrimSpace(*outputPath) == "" {
			return errors.New("--output is required")
		}
		manifest, err := buildManifest(absRoot)
		if err != nil {
			return err
		}
		return writeJSONAtomic(*outputPath, manifest, 0o644)
	case "verify":
		if strings.TrimSpace(*manifestPath) == "" {
			return errors.New("--manifest is required")
		}
		if err := verifyManifest(absRoot, *manifestPath); err != nil {
			return err
		}
		_, err := fmt.Fprintln(output, "release manifest verified")
		return err
	case "get":
		manifest, err := readManifest(*manifestPath)
		if err != nil {
			return err
		}
		record, found := manifest.Components[strings.TrimSpace(*component)]
		if !found {
			return fmt.Errorf("release manifest component is missing: %s", *component)
		}
		_, err = fmt.Fprintln(output, record.SourceSHA256)
		return err
	case "descriptor":
		if strings.TrimSpace(*outputPath) == "" {
			return errors.New("--output is required")
		}
		descriptor, err := buildDescriptor(absRoot, *releaseVersion, *revision, *imagePrefix, *archiveName)
		if err != nil {
			return err
		}
		return writeJSONAtomic(*outputPath, descriptor, 0o644)
	case "deploy-env":
		if strings.TrimSpace(*outputPath) == "" {
			return errors.New("--output is required")
		}
		descriptor, err := buildDescriptor(absRoot, *releaseVersion, *revision, *imagePrefix, *archiveName)
		if err != nil {
			return err
		}
		return writeDeployEnvironment(*outputPath, descriptor)
	default:
		return fmt.Errorf("unsupported manifest command: %s", arguments[0])
	}
}

func runImageMetadata(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("image-metadata", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	inputPath := flags.String("input", "", "docker buildx imagetools JSON input")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*inputPath) == "" {
		return errors.New("--input is required")
	}
	raw, err := os.ReadFile(*inputPath)
	if err != nil {
		return fmt.Errorf("read image metadata: %w", err)
	}
	var metadata buildxImageMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return fmt.Errorf("decode image metadata: %w", err)
	}
	manifestDigest := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(metadata.Manifest.Digest)), "sha256:")
	if !validSHA256(manifestDigest) {
		return errors.New("image metadata manifest digest is invalid")
	}
	labels := metadata.Image.Config.Labels
	component := strings.TrimSpace(labels["io.codex-cpa.component"])
	componentDigest := strings.ToLower(strings.TrimSpace(labels["io.codex-cpa.component-digest"]))
	sourceDigest := strings.ToLower(strings.TrimSpace(labels["io.codex-cpa.source-digest"]))
	if component == "" || !validSHA256(componentDigest) || !validSHA256(sourceDigest) {
		return errors.New("image metadata release labels are invalid")
	}
	_, err = fmt.Fprintf(output, "sha256:%s\t%s\t%s\t%s\n", manifestDigest, component, componentDigest, sourceDigest)
	return err
}

func writeDeployEnvironment(path string, descriptor releaseDescriptor) error {
	values := []struct {
		name  string
		value string
	}{
		{"CPAP_RELEASE_VERSION", descriptor.ReleaseVersion},
		{"CPAP_RELEASE_REVISION", descriptor.Revision},
		{"CPAP_RELEASE_ARCHIVE", descriptor.ArchiveName},
	}
	for _, component := range []string{"control", "web", "gateway", "edge"} {
		record, found := descriptor.Components[component]
		if !found {
			return fmt.Errorf("release descriptor component is missing: %s", component)
		}
		values = append(values, struct {
			name  string
			value string
		}{"CPAP_" + strings.ToUpper(component) + "_IMAGE", record.Image})
	}
	lines := make([]string, 0, len(values))
	for _, value := range values {
		if value.value == "" || strings.ContainsAny(value.value, "\r\n\t ='\"") {
			return fmt.Errorf("release deployment value is not shell-safe: %s", value.name)
		}
		lines = append(lines, value.name+"="+value.value)
	}
	return writeBytesAtomic(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func includedFiles(root string, inputs []string) ([]string, error) {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, input := range inputs {
		candidate := filepath.Join(root, filepath.FromSlash(input))
		information, err := os.Lstat(candidate)
		if err != nil {
			return nil, fmt.Errorf("release component input is missing: %s: %w", input, err)
		}
		if !information.IsDir() {
			seen[filepath.ToSlash(input)] = struct{}{}
			continue
		}
		err = filepath.WalkDir(candidate, func(path string, entry fs.DirEntry, walkError error) error {
			if walkError != nil {
				return walkError
			}
			if entry.IsDir() {
				if path != candidate && (entry.Name() == "node_modules" || entry.Name() == "dist") {
					return filepath.SkipDir
				}
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			seen[filepath.ToSlash(relative)] = struct{}{}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk release component input %s: %w", input, err)
		}
	}
	for relative := range seen {
		result = append(result, relative)
	}
	sort.Strings(result)
	return result, nil
}

func componentDigest(root string, inputs []string) (string, error) {
	files, err := includedFiles(root, inputs)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	for _, relative := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		information, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		var kind string
		var payload []byte
		if information.Mode()&os.ModeSymlink != 0 {
			kind = "symlink"
			target, err := os.Readlink(path)
			if err != nil {
				return "", err
			}
			payload = []byte(target)
		} else if information.Mode().IsRegular() {
			kind = "file"
			payload, err = os.ReadFile(path)
			if err != nil {
				return "", err
			}
		} else {
			return "", fmt.Errorf("unsupported release component input: %s", relative)
		}
		header := fmt.Sprintf("%s\x00%s\x00%04o\x00%d\x00", kind, relative, information.Mode().Perm(), len(payload))
		_, _ = digest.Write([]byte(header))
		_, _ = digest.Write(payload)
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func buildManifest(root string) (releaseManifest, error) {
	manifest := releaseManifest{Version: manifestVersion, Components: make(map[string]componentRecord, len(componentInputs))}
	for _, component := range releaseComponents {
		inputs := componentInputs[component]
		digest, err := componentDigest(root, inputs)
		if err != nil {
			return releaseManifest{}, err
		}
		manifest.Components[component] = componentRecord{SourceSHA256: digest, Inputs: append([]string(nil), inputs...)}
	}
	return manifest, nil
}

func readManifest(path string) (releaseManifest, error) {
	if strings.TrimSpace(path) == "" {
		return releaseManifest{}, errors.New("--manifest is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return releaseManifest{}, fmt.Errorf("read release manifest: %w", err)
	}
	var manifest releaseManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return releaseManifest{}, fmt.Errorf("decode release manifest: %w", err)
	}
	if manifest.Version != manifestVersion {
		return releaseManifest{}, errors.New("unsupported release manifest version")
	}
	for component, inputs := range componentInputs {
		record, found := manifest.Components[component]
		if !found || !equalStrings(record.Inputs, inputs) || !validSHA256(record.SourceSHA256) {
			return releaseManifest{}, fmt.Errorf("release manifest component is invalid: %s", component)
		}
	}
	if len(manifest.Components) != len(componentInputs) {
		return releaseManifest{}, errors.New("release manifest contains unsupported components")
	}
	return manifest, nil
}

func verifyManifest(root string, path string) error {
	expected, err := readManifest(path)
	if err != nil {
		return err
	}
	actual, err := buildManifest(root)
	if err != nil {
		return err
	}
	for component := range componentInputs {
		if expected.Components[component].SourceSHA256 != actual.Components[component].SourceSHA256 {
			return fmt.Errorf("release manifest verification failed: %s", component)
		}
	}
	return nil
}

func buildDescriptor(root, version, revision, prefix, archive string) (releaseDescriptor, error) {
	version = strings.TrimSpace(version)
	revision = strings.ToLower(strings.TrimSpace(revision))
	prefix = strings.TrimSuffix(strings.TrimSpace(prefix), "/")
	archive = strings.TrimSpace(archive)
	if !semanticVersionPattern.MatchString(version) {
		return releaseDescriptor{}, errors.New("release version is invalid")
	}
	if len(revision) < 7 || !isLowerHex(revision) {
		return releaseDescriptor{}, errors.New("release revision is invalid")
	}
	if !registryPrefixPattern.MatchString(prefix) {
		return releaseDescriptor{}, errors.New("release image prefix is invalid")
	}
	if archive == "" || filepath.Base(archive) != archive {
		return releaseDescriptor{}, errors.New("release archive name is invalid")
	}
	manifest, err := buildManifest(root)
	if err != nil {
		return releaseDescriptor{}, err
	}
	descriptor := releaseDescriptor{
		SchemaVersion: releaseDescriptorVersion, ReleaseVersion: version, Revision: revision,
		ImagePrefix: prefix, ArchiveName: archive, Components: make(map[string]descriptorRecord, len(manifest.Components)),
	}
	for component, record := range manifest.Components {
		descriptor.Components[component] = descriptorRecord{
			Image:        fmt.Sprintf("%s/codex-cpa-%s:sha256-%s", prefix, component, record.SourceSHA256),
			SourceSHA256: record.SourceSHA256,
		}
	}
	return descriptor, nil
}

func runPrivacy(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("privacy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", ".", "repository root")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	problems, err := privacyProblems(absRoot)
	if err != nil {
		return err
	}
	if len(problems) != 0 {
		for _, problem := range problems {
			fmt.Fprintln(output, problem)
		}
		return fmt.Errorf("public release check failed: found %d issue(s)", len(problems))
	}
	_, err = fmt.Fprintln(output, "public release check passed")
	return err
}

func privacyProblems(root string) ([]string, error) {
	command := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	raw, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list release inputs: %w", err)
	}
	problems := make(map[string]struct{})
	for _, item := range strings.Split(string(raw), "\x00") {
		if item == "" {
			continue
		}
		relative := filepath.Clean(filepath.FromSlash(item))
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if relative == "AGENTS.md" || containsPart(parts, ".harness") {
			problems[item+": local agent or deployment context is tracked"] = struct{}{}
			continue
		}
		if containsAnyPart(parts, []string{"auth", "backups", "configs", "logs", "secrets", "state"}) {
			problems[item+": runtime or secret path is tracked"] = struct{}{}
			continue
		}
		path := filepath.Join(root, relative)
		information, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !information.Mode().IsRegular() || information.Size() > maxReleaseText {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if strings.IndexByte(string(content), 0) >= 0 {
			continue
		}
		text := string(content)
		for _, marker := range []string{
			"-----BEGIN " + "PRIVATE KEY-----",
			"-----BEGIN OPENSSH " + "PRIVATE KEY-----",
			"-----BEGIN RSA " + "PRIVATE KEY-----",
		} {
			if strings.Contains(text, marker) {
				problems[item+": contains private key material"] = struct{}{}
			}
		}
		if containsWebhookSecret(text) {
			problems[item+": contains a Webhook secret"] = struct{}{}
		}
		for _, match := range ipv4Pattern.FindAllStringSubmatch(text, -1) {
			address := net.ParseIP(match[1])
			if address != nil && address.IsPrivate() {
				problems[item+": contains a private IP address"] = struct{}{}
				break
			}
		}
		for _, match := range emailPattern.FindAllStringSubmatch(text, -1) {
			if !allowedDomain(match[1]) {
				problems[item+": contains a non-example email domain"] = struct{}{}
				break
			}
		}
		for _, pattern := range []*regexp.Regexp{urlPattern, registryHostPattern} {
			for _, match := range pattern.FindAllStringSubmatch(text, -1) {
				if !allowedDomain(match[1]) {
					problems[item+": contains an unapproved fixed domain"] = struct{}{}
					break
				}
			}
		}
	}
	result := make([]string, 0, len(problems))
	for problem := range problems {
		result = append(result, problem)
	}
	sort.Strings(result)
	return result, nil
}

func runArchive(arguments []string, output io.Writer) error {
	if len(arguments) != 2 || arguments[0] != "verify" {
		return errors.New("usage: cpa-releasectl archive verify <archive.tar.gz>")
	}
	if err := verifyArchive(arguments[1]); err != nil {
		return err
	}
	_, err := fmt.Fprintln(output, "release archive verified")
	return err
}

func verifyArchive(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open release archive: %w", err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read release archive: %w", err)
		}
		name := filepath.ToSlash(header.Name)
		if strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
			return fmt.Errorf("release archive contains unsafe path: %s", name)
		}
		if isRemovedRuntimePath(name) {
			return fmt.Errorf("release archive contains removed runtime path: %s", name)
		}
		if isGeneratedReleaseArtifactPath(name) {
			return fmt.Errorf("release archive contains generated test or build artifact: %s", name)
		}
		for _, part := range strings.Split(name, "/") {
			if strings.HasPrefix(part, "._") {
				return fmt.Errorf("release archive contains Apple metadata: %s", name)
			}
		}
		for key := range header.PAXRecords {
			if strings.Contains(strings.ToLower(key), "xattr") {
				return fmt.Errorf("release archive contains extended attributes: %s", name)
			}
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeSymlink && header.Typeflag != tar.TypeLink {
			return fmt.Errorf("release archive contains unsupported file type: %s", name)
		}
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			target, err := url.PathUnescape(header.Linkname)
			if err != nil || strings.HasPrefix(target, "/") {
				return fmt.Errorf("release archive contains unsafe link: %s", name)
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(name), filepath.FromSlash(target)))
			if resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
				return fmt.Errorf("release archive contains unsafe link: %s", name)
			}
		}
	}
}

func isGeneratedReleaseArtifactPath(name string) bool {
	cleaned := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(name)), "./")
	for _, part := range strings.Split(cleaned, "/") {
		switch strings.ToLower(part) {
		case "coverage", "dist", "node_modules", "playwright-report", "test-results":
			return true
		}
	}
	return false
}

func isRemovedRuntimePath(name string) bool {
	cleaned := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(name)), "./")
	base := strings.ToLower(filepath.Base(cleaned))
	if strings.HasSuffix(base, ".py") || strings.HasSuffix(base, ".pyc") || strings.HasSuffix(base, ".lua") {
		return true
	}
	if strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt") {
		return true
	}
	removedPrefixes := []string{
		"cmd/docker-read-" + "proxy",
		"internal/dockerread" + "proxy",
		"release/Docker" + "file",
		"testdata/v" + "2",
		"v" + "2",
	}
	for _, prefix := range removedPrefixes {
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
			return true
		}
	}
	top, _, _ := strings.Cut(cleaned, "/")
	switch top {
	case "admin", "dashboard", "edge", "gateway", "portal", "tests", "web":
		return true
	default:
		return false
	}
}

func runChecksum(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("checksum", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	outputPath := flags.String("output", "", "optional SHA256SUMS output path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	paths := flags.Args()
	if len(paths) == 0 {
		return errors.New("checksum requires at least one file")
	}
	lines := make([]string, 0, len(paths))
	for _, path := range paths {
		information, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect checksum input %s: %w", path, err)
		}
		if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
			return fmt.Errorf("checksum input must be a regular non-symlink file: %s", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open checksum input %s: %w", path, err)
		}
		digest := sha256.New()
		_, copyError := io.Copy(digest, file)
		closeError := file.Close()
		if copyError != nil {
			return fmt.Errorf("hash checksum input %s: %w", path, copyError)
		}
		if closeError != nil {
			return fmt.Errorf("close checksum input %s: %w", path, closeError)
		}
		lines = append(lines, fmt.Sprintf(
			"%s  %s",
			hex.EncodeToString(digest.Sum(nil)),
			filepath.Base(path),
		))
	}
	payload := []byte(strings.Join(lines, "\n") + "\n")
	if strings.TrimSpace(*outputPath) == "" {
		_, err := output.Write(payload)
		return err
	}
	return writeBytesAtomic(*outputPath, payload, 0o644)
}

func writeJSONAtomic(path string, value any, mode fs.FileMode) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(absPath), "."+filepath.Base(absPath)+".*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, absPath)
}

func writeBytesAtomic(path string, payload []byte, mode fs.FileMode) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(absPath), "."+filepath.Base(absPath)+".*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, absPath)
}

func validSHA256(value string) bool { return len(value) == 64 && isLowerHex(value) }
func isLowerHex(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return value != ""
}
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
func containsPart(parts []string, expected string) bool {
	for _, part := range parts {
		if part == expected {
			return true
		}
	}
	return false
}
func containsAnyPart(parts, expected []string) bool {
	for _, item := range expected {
		if containsPart(parts, item) {
			return true
		}
	}
	return false
}
func allowedDomain(host string) bool {
	normalized := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	// Public community links use Telegram's exact short-link host.
	if normalized == "t.me" || normalized == "api.telegram.org" {
		return true
	}
	for _, suffix := range allowedDomainSuffixes {
		if normalized == suffix || strings.HasSuffix(normalized, "."+suffix) {
			return true
		}
	}
	return false
}

func containsWebhookSecret(content string) bool {
	for _, match := range webhookSecretPattern.FindAllStringSubmatch(content, -1) {
		key := strings.ToLower(match[1])
		if strings.HasPrefix(key, "test-") || strings.HasPrefix(key, "test_") ||
			strings.HasPrefix(key, "example-") || strings.HasPrefix(key, "example_") ||
			strings.HasPrefix(key, "placeholder-") || strings.HasPrefix(key, "placeholder_") {
			continue
		}
		return true
	}
	return false
}

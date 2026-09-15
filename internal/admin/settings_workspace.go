package admin

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/notifications"
	"github.com/gin-gonic/gin"
)

const settingsAuditLimit = 20

var auditSecretPattern = regexp.MustCompile(`(?i)(?:\b(?:sk|key)-[a-z0-9_-]{12,}\b|\b(?:ccpa|cpa)_[a-z0-9_-]{12,}\b|\bbearer\s+[a-z0-9._~+/=-]{12,})`)

type settingsWorkspaceStorage struct {
	Label  string `json:"label"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Mode   string `json:"mode"`
}

type settingsWorkspaceBackups struct {
	Count  int    `json:"count"`
	Latest string `json:"latest"`
}

type settingsWorkspaceAudit struct {
	Timestamp int64  `json:"timestamp"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	Outcome   string `json:"outcome"`
}

type settingsWorkspaceResponse struct {
	Storage     []settingsWorkspaceStorage `json:"storage"`
	Backups     settingsWorkspaceBackups   `json:"backups"`
	RecentAudit []settingsWorkspaceAudit   `json:"recent_audit"`
}

func (server *Server) readSettingsWorkspace(c *gin.Context) {
	if server.root == "" {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.configuration_center_runtime_directory_is_unavailable"), "settings_workspace_unavailable")
		return
	}
	payload, err := inspectSettingsWorkspace(server.root, httpi18n.Locale(c))
	if err != nil {
		server.internalError(c, "read settings workspace", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, payload)
}

func inspectSettingsWorkspace(root string, languages ...i18n.Language) (settingsWorkspaceResponse, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return settingsWorkspaceResponse{}, fmt.Errorf("resolve settings workspace root: %w", err)
	}
	absoluteRoot = filepath.Clean(absoluteRoot)
	backups, err := inspectSettingsBackups(absoluteRoot)
	if err != nil {
		return settingsWorkspaceResponse{}, err
	}
	storageDefinitions := []struct{ label, relative string }{
		{i18n.Text(i18n.Selected(languages), "admin.control_plane_database"), "state/control-plane.sqlite3"},
		{i18n.Text(i18n.Selected(languages), "admin.user_usage_database"), "state/usage.sqlite3"},
		{i18n.Text(i18n.Selected(languages), "admin.control_plane_encryption_key"), "secrets/control-plane.key"},
		{i18n.Text(i18n.Selected(languages), "admin.admin_audit_log"), "logs/admin/audit.jsonl"},
	}
	storage := make([]settingsWorkspaceStorage, 0, len(storageDefinitions))
	for _, definition := range storageDefinitions {
		item, inspectError := inspectSettingsStorage(absoluteRoot, definition.label, definition.relative)
		if inspectError != nil {
			return settingsWorkspaceResponse{}, inspectError
		}
		storage = append(storage, item)
	}
	audit, err := readSettingsAudit(filepath.Join(absoluteRoot, "logs", "admin", "audit.jsonl"))
	if err != nil {
		return settingsWorkspaceResponse{}, err
	}
	return settingsWorkspaceResponse{Storage: storage, Backups: backups, RecentAudit: audit}, nil
}

func inspectSettingsBackups(root string) (settingsWorkspaceBackups, error) {
	backupRoot := filepath.Join(root, "backups", "accounts")
	entries, err := os.ReadDir(backupRoot)
	if errors.Is(err, os.ErrNotExist) {
		return settingsWorkspaceBackups{}, nil
	}
	if err != nil {
		return settingsWorkspaceBackups{}, fmt.Errorf("read account backup directory: %w", err)
	}
	type backupEntry struct {
		name    string
		modTime int64
	}
	backups := make([]backupEntry, 0, len(entries))
	for _, entry := range entries {
		information, informationError := entry.Info()
		if informationError != nil {
			return settingsWorkspaceBackups{}, fmt.Errorf("inspect account backup %s: %w", entry.Name(), informationError)
		}
		if !information.IsDir() || information.Mode()&os.ModeSymlink != 0 {
			continue
		}
		backups = append(backups, backupEntry{name: entry.Name(), modTime: information.ModTime().UnixNano()})
	}
	sort.Slice(backups, func(left, right int) bool {
		if backups[left].modTime == backups[right].modTime {
			return backups[left].name > backups[right].name
		}
		return backups[left].modTime > backups[right].modTime
	})
	result := settingsWorkspaceBackups{Count: len(backups)}
	if len(backups) > 0 {
		result.Latest = filepath.ToSlash(filepath.Join("backups", "accounts", backups[0].name))
	}
	return result, nil
}

func inspectSettingsStorage(root string, label string, relative string) (settingsWorkspaceStorage, error) {
	item := settingsWorkspaceStorage{Label: label, Path: filepath.ToSlash(relative), Mode: "—"}
	information, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative)))
	if errors.Is(err, os.ErrNotExist) {
		return item, nil
	}
	if err != nil {
		return item, fmt.Errorf("inspect settings storage %s: %w", relative, err)
	}
	item.Exists = information.Mode().IsRegular() && information.Mode()&os.ModeSymlink == 0
	item.Mode = fmt.Sprintf("%03o", information.Mode().Perm())
	return item, nil
}

func readSettingsAudit(path string) ([]settingsWorkspaceAudit, error) {
	information, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return []settingsWorkspaceAudit{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect Admin audit log: %w", err)
	}
	if !information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Admin audit log must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Admin audit log: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 16*1024), 1<<20)
	records := make([]settingsWorkspaceAudit, 0, settingsAuditLimit)
	for scanner.Scan() {
		var source struct {
			Timestamp int64  `json:"timestamp"`
			Action    string `json:"action"`
			Target    string `json:"target"`
			Outcome   string `json:"outcome"`
		}
		if json.Unmarshal(scanner.Bytes(), &source) != nil {
			continue
		}
		record := settingsWorkspaceAudit{
			Timestamp: source.Timestamp,
			Action:    sanitizeAuditText(source.Action),
			Target:    sanitizeAuditText(source.Target),
			Outcome:   sanitizeAuditText(source.Outcome),
		}
		if len(records) == settingsAuditLimit {
			copy(records, records[1:])
			records[len(records)-1] = record
		} else {
			records = append(records, record)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Admin audit log: %w", err)
	}
	for left, right := 0, len(records)-1; left < right; left, right = left+1, right-1 {
		records[left], records[right] = records[right], records[left]
	}
	return records, nil
}

func sanitizeAuditText(value string) string {
	value = notifications.RedactWebhook(value)
	value = auditSecretPattern.ReplaceAllString(value, "[REDACTED]")
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	characters := []rune(value)
	if len(characters) > 512 {
		value = string(characters[:512]) + "…"
	}
	return value
}

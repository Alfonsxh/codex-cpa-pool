package controlplane

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

const (
	databaseRelativePath = "state/control-plane.sqlite3"
	lockRelativePath     = "state/.control-plane.lock"
	keyRelativePath      = "secrets/control-plane.key"
)

var legacySecretFiles = map[string]string{
	"cpa_management_key": "cpa-management.key",
	"wecom_webhook":      "wecom-webhook.url",
}

type Options struct {
	SecretKeyPath string
	Now           func() time.Time
}

type Store struct {
	root     string
	path     string
	keyPath  string
	lockPath string
	db       *sqlx.DB
	writeDB  *sqlx.DB
	now      func() time.Time

	keyMu sync.Mutex
	key   []byte

	initializeMu sync.Mutex
	initialized  bool

	writeFenceMu sync.RWMutex
	writeFence   []Lease
}

func Open(ctx context.Context, root string, options Options) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("control-plane root is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve control-plane root: %w", err)
	}
	absoluteRoot = filepath.Clean(absoluteRoot)
	stateDirectory := filepath.Join(absoluteRoot, "state")
	secretsDirectory := filepath.Join(absoluteRoot, "secrets")
	for _, directory := range []string{stateDirectory, secretsDirectory} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create control-plane directory %s: %w", directory, err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, fmt.Errorf("secure control-plane directory %s: %w", directory, err)
		}
	}

	keyPath := strings.TrimSpace(options.SecretKeyPath)
	if keyPath == "" {
		keyPath = strings.TrimSpace(os.Getenv("CLIPROXY_SECRET_KEY_FILE"))
	}
	if keyPath == "" {
		keyPath = filepath.Join(absoluteRoot, keyRelativePath)
	} else if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(absoluteRoot, keyPath)
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	path := filepath.Join(absoluteRoot, databaseRelativePath)
	database, err := openSQLiteHandle(path, "", false)
	if err != nil {
		return nil, fmt.Errorf("open control-plane database: %w", err)
	}
	writeDatabase, err := openSQLiteHandle(path, "", true)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("open control-plane write database: %w", err)
	}
	store := &Store{
		root:     absoluteRoot,
		path:     path,
		keyPath:  filepath.Clean(keyPath),
		lockPath: filepath.Join(absoluteRoot, lockRelativePath),
		db:       database,
		writeDB:  writeDatabase,
		now:      options.Now,
	}
	if err := store.InitializeExisting(ctx); err != nil {
		_ = database.Close()
		_ = writeDatabase.Close()
		return nil, err
	}
	return store, nil
}

// OpenExisting opens the authoritative control-plane database in read-write
// mode without creating directories, a database, an encryption key, schema
// objects, or legacy-secret projections. Mutable runtimes use this restricted
// handle only to acquire ownership leases. They must call InitializeExisting
// after the runtime and worker leases have both been acquired.
func OpenExisting(ctx context.Context, root string, options Options) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("control-plane root is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve control-plane root: %w", err)
	}
	absoluteRoot = filepath.Clean(absoluteRoot)
	path := filepath.Join(absoluteRoot, databaseRelativePath)
	if err := requireExistingRegularFile(path, "control-plane database"); err != nil {
		return nil, err
	}

	keyPath := strings.TrimSpace(options.SecretKeyPath)
	if keyPath == "" {
		keyPath = strings.TrimSpace(os.Getenv("CLIPROXY_SECRET_KEY_FILE"))
	}
	if keyPath == "" {
		keyPath = filepath.Join(absoluteRoot, keyRelativePath)
	} else if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(absoluteRoot, keyPath)
	}
	keyPath = filepath.Clean(keyPath)
	if err := requireExistingRegularFile(keyPath, "control-plane encryption key"); err != nil {
		return nil, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	database, err := openSQLiteHandle(path, "rw", false)
	if err != nil {
		return nil, fmt.Errorf("open existing control-plane database: %w", err)
	}
	writeDatabase, err := openSQLiteHandle(path, "rw", true)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("open existing control-plane write database: %w", err)
	}
	store := &Store{
		root:     absoluteRoot,
		path:     path,
		keyPath:  keyPath,
		lockPath: filepath.Join(absoluteRoot, lockRelativePath),
		db:       database,
		writeDB:  writeDatabase,
		now:      options.Now,
	}
	if err := store.configureForLease(ctx); err != nil {
		_ = database.Close()
		_ = writeDatabase.Close()
		return nil, err
	}
	var runtimeStateTable int
	if err := store.db.GetContext(
		ctx,
		&runtimeStateTable,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'runtime_state'",
	); err != nil {
		_ = database.Close()
		_ = writeDatabase.Close()
		return nil, fmt.Errorf("inspect existing control-plane database: %w", err)
	}
	if runtimeStateTable != 1 {
		_ = database.Close()
		_ = writeDatabase.Close()
		return nil, errors.New("existing control-plane database is missing required table runtime_state")
	}
	return store, nil
}

func openSQLiteHandle(path string, mode string, immediateWrites bool) (*sqlx.DB, error) {
	dsn := &url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	if mode != "" {
		query.Set("mode", mode)
	}
	if immediateWrites {
		// Every transaction on this handle starts as BEGIN IMMEDIATE. The
		// busy handler therefore waits before the Lease read snapshot is
		// established instead of failing during a deferred read-to-write
		// upgrade. Read-only transactions continue to use Store.db.
		query.Set("_txlock", "immediate")
	}
	dsn.RawQuery = query.Encode()
	database, err := sqlx.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	return database, nil
}

func requireExistingRegularFile(path string, description string) error {
	information, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("open existing %s: %w", description, err)
	}
	if !information.Mode().IsRegular() {
		return fmt.Errorf("open existing %s: %s is not a regular file", description, path)
	}
	return nil
}

func (store *Store) Root() string {
	return store.root
}

func (store *Store) Path() string {
	return store.path
}

func (store *Store) SecretKeyPath() string {
	return store.keyPath
}

func (store *Store) Close() error {
	if store == nil {
		return nil
	}
	var readError, writeError error
	if store.db != nil {
		readError = store.db.Close()
	}
	if store.writeDB != nil {
		writeError = store.writeDB.Close()
	}
	return errors.Join(readError, writeError)
}

func (store *Store) configureForLease(ctx context.Context) error {
	for _, database := range []*sqlx.DB{store.db, store.writeDB} {
		for _, statement := range []string{
			"PRAGMA busy_timeout = 30000",
			"PRAGMA foreign_keys = ON",
		} {
			if _, err := database.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("configure ownership lease database with %q: %w", statement, err)
			}
		}
	}
	return nil
}

func (store *Store) configure(ctx context.Context) error {
	for _, database := range []*sqlx.DB{store.db, store.writeDB} {
		for _, statement := range []string{
			"PRAGMA busy_timeout = 30000",
			"PRAGMA foreign_keys = ON",
			"PRAGMA journal_mode = WAL",
			"PRAGMA synchronous = FULL",
		} {
			if _, err := database.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("configure control-plane database with %q: %w", statement, err)
			}
		}
	}
	return nil
}

// InitializeExisting enables the full mutable-store configuration, validates
// and migrates the schema, verifies the existing encryption key, and imports
// any supported legacy secrets. Callers of OpenExisting must invoke it only
// while protected by the shared runtime and process-level worker leases.
func (store *Store) InitializeExisting(ctx context.Context) error {
	store.initializeMu.Lock()
	defer store.initializeMu.Unlock()
	if store.initialized {
		return nil
	}
	if err := store.configure(ctx); err != nil {
		return err
	}
	if err := store.initialize(ctx); err != nil {
		return err
	}
	store.initialized = true
	return nil
}

func (store *Store) initialize(ctx context.Context) error {
	if err := store.exclusive(func() error {
		transaction, err := store.writeDB.BeginTxx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin control-plane initialization: %w", err)
		}
		defer transaction.Rollback()
		if err := store.validateWriteFence(ctx, transaction); err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, schemaSQL); err != nil {
			return fmt.Errorf("initialize control-plane schema: %w", err)
		}
		if err := migrateAccountsSchema(ctx, transaction); err != nil {
			return err
		}
		if err := migrateTeamsSchema(ctx, transaction); err != nil {
			return err
		}
		if err := validateSchema(ctx, transaction); err != nil {
			return err
		}
		var version int
		if err := transaction.GetContext(
			ctx,
			&version,
			"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
		); err != nil {
			return fmt.Errorf("read control-plane schema version: %w", err)
		}
		if version > SchemaVersion {
			return fmt.Errorf(
				"control-plane schema version %d is newer than supported version %d",
				version,
				SchemaVersion,
			)
		}
		if version < SchemaVersion {
			if _, err := transaction.ExecContext(
				ctx,
				"DELETE FROM encrypted_secrets WHERE name = ?",
				"gost_tunnel_auth",
			); err != nil {
				return fmt.Errorf("delete retired control-plane secret: %w", err)
			}
		}
		if _, err := transaction.ExecContext(
			ctx,
			"INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (?, ?)",
			SchemaVersion,
			store.now().Unix(),
		); err != nil {
			return fmt.Errorf("record control-plane schema version: %w", err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit control-plane initialization: %w", err)
		}
		if err := os.Chmod(store.path, 0o600); err != nil {
			return fmt.Errorf("secure control-plane database: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	if _, err := store.encryptionKey(ctx); err != nil {
		return err
	}
	return store.importLegacySecrets(ctx)
}

func migrateAccountsSchema(ctx context.Context, transaction *sqlx.Tx) error {
	columns, err := tableColumns(ctx, transaction, "accounts")
	if err != nil {
		return err
	}
	if _, ok := columns["proxy_mode"]; !ok {
		if _, err := transaction.ExecContext(
			ctx,
			"ALTER TABLE accounts ADD COLUMN proxy_mode TEXT NOT NULL DEFAULT 'inherit'",
		); err != nil {
			return fmt.Errorf("add accounts proxy_mode column: %w", err)
		}
		columns["proxy_mode"] = struct{}{}
	}
	allowed := make(map[string]struct{}, len(expectedAccountColumns)+len(retiredAccountColumns))
	for name := range expectedAccountColumns {
		allowed[name] = struct{}{}
	}
	for name := range retiredAccountColumns {
		allowed[name] = struct{}{}
	}
	missing := setDifference(expectedAccountColumns, columns)
	unexpected := setDifference(columns, allowed)
	if len(missing) > 0 || len(unexpected) > 0 {
		return fmt.Errorf(
			"unsupported accounts table: missing [%s], unexpected [%s]",
			strings.Join(missing, ", "),
			strings.Join(unexpected, ", "),
		)
	}
	if _, retired := columns["gost_port"]; retired {
		if _, err := transaction.ExecContext(ctx, rebuildAccountsSQL); err != nil {
			return fmt.Errorf("remove retired accounts columns: %w", err)
		}
	}
	return nil
}

func migrateTeamsSchema(ctx context.Context, transaction *sqlx.Tx) error {
	columns, err := tableColumns(ctx, transaction, "teams")
	if err != nil {
		return err
	}
	if _, ok := columns["tag_style"]; ok {
		return nil
	}
	if _, err := transaction.ExecContext(
		ctx,
		`ALTER TABLE teams ADD COLUMN tag_style TEXT NOT NULL DEFAULT 'indigo'
            CHECK(tag_style IN ('indigo', 'blue', 'cyan', 'teal', 'green', 'amber', 'orange', 'rose', 'violet', 'slate'))`,
	); err != nil {
		return fmt.Errorf("add teams tag_style column: %w", err)
	}
	teamIDs := make([]string, 0)
	if err := transaction.SelectContext(
		ctx,
		&teamIDs,
		"SELECT id FROM teams ORDER BY created_at, id",
	); err != nil {
		return fmt.Errorf("list teams for tag style migration: %w", err)
	}
	for index, teamID := range teamIDs {
		if _, err := transaction.ExecContext(
			ctx,
			"UPDATE teams SET tag_style = ? WHERE id = ?",
			teamTagStyles[index%len(teamTagStyles)],
			teamID,
		); err != nil {
			return fmt.Errorf("migrate team %s tag style: %w", teamID, err)
		}
	}
	return nil
}

func validateSchema(ctx context.Context, transaction *sqlx.Tx) error {
	tables := make(map[string]struct{})
	rows, err := transaction.QueryxContext(
		ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table'",
	)
	if err != nil {
		return fmt.Errorf("list control-plane tables: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("scan control-plane table: %w", err)
		}
		tables[name] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close control-plane table list: %w", err)
	}
	for _, name := range requiredTables {
		if _, ok := tables[name]; !ok {
			return fmt.Errorf("control-plane database is missing required table %s", name)
		}
	}

	columns, err := tableColumns(ctx, transaction, "accounts")
	if err != nil {
		return err
	}
	missing := setDifference(expectedAccountColumns, columns)
	unexpected := setDifference(columns, expectedAccountColumns)
	if len(missing) > 0 || len(unexpected) > 0 {
		return fmt.Errorf(
			"unsupported accounts table: missing [%s], unexpected [%s]",
			strings.Join(missing, ", "),
			strings.Join(unexpected, ", "),
		)
	}
	columns, err = tableColumns(ctx, transaction, "teams")
	if err != nil {
		return err
	}
	missing = setDifference(expectedTeamColumns, columns)
	unexpected = setDifference(columns, expectedTeamColumns)
	if len(missing) > 0 || len(unexpected) > 0 {
		return fmt.Errorf(
			"unsupported teams table: missing [%s], unexpected [%s]",
			strings.Join(missing, ", "),
			strings.Join(unexpected, ", "),
		)
	}
	var invalidTeamStyles int
	if err := transaction.GetContext(
		ctx,
		&invalidTeamStyles,
		`SELECT COUNT(*) FROM teams
		  WHERE tag_style IS NULL
             OR tag_style NOT IN ('indigo', 'blue', 'cyan', 'teal', 'green', 'amber', 'orange', 'rose', 'violet', 'slate')`,
	); err != nil {
		return fmt.Errorf("validate team tag styles: %w", err)
	}
	if invalidTeamStyles > 0 {
		return fmt.Errorf("unsupported teams table: %d invalid tag styles", invalidTeamStyles)
	}
	return nil
}

func tableColumns(ctx context.Context, transaction *sqlx.Tx, table string) (map[string]struct{}, error) {
	columns := make(map[string]struct{})
	rows, err := transaction.QueryxContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, fmt.Errorf("inspect %s table: %w", table, err)
	}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan %s table column: %w", table, err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close %s table inspection: %w", table, err)
	}
	return columns, nil
}

func setDifference(left map[string]struct{}, right map[string]struct{}) []string {
	result := make([]string, 0)
	for value := range left {
		if _, ok := right[value]; !ok {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func (store *Store) exclusive(operation func() error) error {
	lock, err := os.OpenFile(store.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open control-plane lock: %w", err)
	}
	defer lock.Close()
	if err := os.Chmod(store.lockPath, 0o600); err != nil {
		return fmt.Errorf("secure control-plane lock: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock control-plane state: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	return operation()
}

func (store *Store) writeTransaction(
	ctx context.Context,
	operation func(*sqlx.Tx) error,
) error {
	return store.exclusive(func() error {
		transaction, err := store.writeDB.BeginTxx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin control-plane transaction: %w", err)
		}
		defer transaction.Rollback()
		if err := store.validateWriteFence(ctx, transaction); err != nil {
			return err
		}
		if err := operation(transaction); err != nil {
			return err
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit control-plane transaction: %w", err)
		}
		return nil
	})
}

func (store *Store) encryptionKey(ctx context.Context) ([]byte, error) {
	store.keyMu.Lock()
	defer store.keyMu.Unlock()
	if len(store.key) > 0 {
		return append([]byte(nil), store.key...), nil
	}
	key, err := os.ReadFile(store.keyPath)
	if errors.Is(err, os.ErrNotExist) {
		var encryptedCount int
		if queryError := store.db.GetContext(
			ctx,
			&encryptedCount,
			"SELECT COUNT(*) FROM encrypted_secrets",
		); queryError != nil {
			return nil, fmt.Errorf("count encrypted control-plane secrets: %w", queryError)
		}
		if encryptedCount > 0 {
			return nil, fmt.Errorf(
				"control-plane encryption key is missing while %d encrypted secrets exist: %s",
				encryptedCount,
				store.keyPath,
			)
		}
		if err := os.MkdirAll(filepath.Dir(store.keyPath), 0o700); err != nil {
			return nil, fmt.Errorf("create control-plane key directory: %w", err)
		}
		if err := os.Chmod(filepath.Dir(store.keyPath), 0o700); err != nil {
			return nil, fmt.Errorf("secure control-plane key directory: %w", err)
		}
		key = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, fmt.Errorf("generate control-plane encryption key: %w", err)
		}
		file, createError := os.OpenFile(store.keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(createError, os.ErrExist) {
			key, err = os.ReadFile(store.keyPath)
			if err != nil {
				return nil, fmt.Errorf("read concurrently-created control-plane key: %w", err)
			}
		} else if createError != nil {
			return nil, fmt.Errorf("create control-plane encryption key: %w", createError)
		} else {
			if _, err := file.Write(key); err != nil {
				file.Close()
				return nil, fmt.Errorf("write control-plane encryption key: %w", err)
			}
			if err := file.Sync(); err != nil {
				file.Close()
				return nil, fmt.Errorf("sync control-plane encryption key: %w", err)
			}
			if err := file.Close(); err != nil {
				return nil, fmt.Errorf("close control-plane encryption key: %w", err)
			}
		}
	} else if err != nil {
		return nil, fmt.Errorf("read control-plane encryption key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("control-plane encryption key must be exactly 32 bytes: %s", store.keyPath)
	}
	information, err := os.Stat(store.keyPath)
	if err != nil {
		return nil, fmt.Errorf("inspect control-plane encryption key: %w", err)
	}
	if information.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("control-plane encryption key permissions must be 0600: %s", store.keyPath)
	}
	store.key = append([]byte(nil), key...)
	return append([]byte(nil), key...), nil
}

func (store *Store) importLegacySecrets(ctx context.Context) error {
	names := make([]string, 0, len(legacySecretFiles))
	for name := range legacySecretFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var count int
		if err := store.db.GetContext(
			ctx,
			&count,
			"SELECT COUNT(*) FROM encrypted_secrets WHERE name = ?",
			name,
		); err != nil {
			return fmt.Errorf("check legacy control-plane secret %s: %w", name, err)
		}
		if count > 0 {
			continue
		}
		path := filepath.Join(store.root, "secrets", legacySecretFiles[name])
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read legacy control-plane secret %s: %w", name, err)
		}
		value := strings.TrimSpace(string(raw))
		if value == "" {
			continue
		}
		if err := store.WriteSecret(ctx, name, value); err != nil {
			return fmt.Errorf("import legacy control-plane secret %s: %w", name, err)
		}
	}
	return nil
}

func secretDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (store *Store) encryptSecret(ctx context.Context, name string, value string) ([]byte, []byte, string, error) {
	key, err := store.encryptionKey(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, "", fmt.Errorf("create control-plane secret cipher: %w", err)
	}
	var authenticated cipher.AEAD
	authenticated, err = cipher.NewGCM(block)
	if err != nil {
		return nil, nil, "", fmt.Errorf("create control-plane AES-GCM: %w", err)
	}
	nonce := make([]byte, authenticated.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, "", fmt.Errorf("generate control-plane secret nonce: %w", err)
	}
	ciphertext := authenticated.Seal(nil, nonce, []byte(value), []byte(name))
	return nonce, ciphertext, secretDigest(value), nil
}

func (store *Store) decryptSecret(ctx context.Context, name string, nonce []byte, ciphertext []byte, digest string) (string, error) {
	key, err := store.encryptionKey(ctx)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create control-plane secret cipher: %w", err)
	}
	authenticated, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create control-plane AES-GCM: %w", err)
	}
	plaintext, err := authenticated.Open(nil, nonce, ciphertext, []byte(name))
	if err != nil {
		return "", fmt.Errorf("control-plane secret %s cannot be decrypted: %w", name, err)
	}
	value := string(plaintext)
	if secretDigest(value) != digest {
		return "", fmt.Errorf("control-plane secret %s integrity digest mismatch", name)
	}
	return value, nil
}

func (store *Store) ReadSecret(ctx context.Context, name string) (string, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false, errors.New("control-plane secret name is required")
	}
	var row struct {
		Name       string `db:"name"`
		Nonce      []byte `db:"nonce"`
		Ciphertext []byte `db:"ciphertext"`
		Digest     string `db:"value_sha256"`
	}
	err := store.db.GetContext(
		ctx,
		&row,
		"SELECT name, nonce, ciphertext, value_sha256 FROM encrypted_secrets WHERE name = ?",
		name,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read control-plane secret %s: %w", name, err)
	}
	value, err := store.decryptSecret(ctx, row.Name, row.Nonce, row.Ciphertext, row.Digest)
	return value, err == nil, err
}

func (store *Store) WriteSecret(ctx context.Context, name string, value string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("control-plane secret name is required")
	}
	if value == "" {
		return fmt.Errorf("control-plane secret %s cannot be empty", name)
	}
	nonce, ciphertext, digest, err := store.encryptSecret(ctx, name, value)
	if err != nil {
		return err
	}
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		_, err := transaction.ExecContext(ctx, `
            INSERT INTO encrypted_secrets(name, nonce, ciphertext, value_sha256, updated_at)
            VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(name) DO UPDATE SET
                nonce = excluded.nonce,
                ciphertext = excluded.ciphertext,
                value_sha256 = excluded.value_sha256,
                updated_at = excluded.updated_at`,
			name, nonce, ciphertext, digest, store.now().Unix(),
		)
		if err != nil {
			return fmt.Errorf("write control-plane secret %s: %w", name, err)
		}
		return nil
	})
}

func (store *Store) DeleteSecret(ctx context.Context, name string) error {
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if _, err := transaction.ExecContext(
			ctx,
			"DELETE FROM encrypted_secrets WHERE name = ?",
			strings.TrimSpace(name),
		); err != nil {
			return fmt.Errorf("delete control-plane secret: %w", err)
		}
		return nil
	})
}

func (store *Store) SecretStatuses(ctx context.Context) (map[string]SecretStatus, error) {
	return secretStatuses(ctx, store.db)
}

func secretStatuses(ctx context.Context, database sqlx.QueryerContext) (map[string]SecretStatus, error) {
	rows := make([]struct {
		Name      string `db:"name"`
		SHA256    string `db:"value_sha256"`
		UpdatedAt int64  `db:"updated_at"`
	}, 0)
	if err := sqlx.SelectContext(
		ctx, database,
		&rows,
		"SELECT name, value_sha256, updated_at FROM encrypted_secrets ORDER BY name",
	); err != nil {
		return nil, fmt.Errorf("list control-plane secret statuses: %w", err)
	}
	result := make(map[string]SecretStatus, len(rows))
	for _, row := range rows {
		result[row.Name] = SecretStatus{SHA256: row.SHA256, UpdatedAt: row.UpdatedAt}
	}
	return result, nil
}

func (store *Store) ReadMetadata(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := store.db.GetContext(ctx, &value, "SELECT value FROM metadata WHERE key = ?", key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read control-plane metadata %s: %w", key, err)
	}
	return value, true, nil
}

func (store *Store) WriteMetadata(ctx context.Context, key string, value string) error {
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		_, err := transaction.ExecContext(ctx, `
            INSERT INTO metadata(key, value) VALUES (?, ?)
            ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
		if err != nil {
			return fmt.Errorf("write control-plane metadata %s: %w", key, err)
		}
		return nil
	})
}

func (store *Store) ReadSettings(ctx context.Context) (map[string]any, error) {
	return readSettings(ctx, store.db)
}

func readSettings(ctx context.Context, database sqlx.QueryerContext) (map[string]any, error) {
	rows := make([]struct {
		Key   string `db:"key"`
		Value string `db:"value_json"`
	}, 0)
	if err := sqlx.SelectContext(
		ctx,
		database,
		&rows,
		"SELECT key, value_json FROM settings ORDER BY key",
	); err != nil {
		return nil, fmt.Errorf("read control-plane settings: %w", err)
	}
	result := make(map[string]any, len(rows))
	for _, row := range rows {
		var value any
		if err := json.Unmarshal([]byte(row.Value), &value); err != nil {
			return nil, fmt.Errorf("decode control-plane setting %s: %w", row.Key, err)
		}
		result[row.Key] = value
	}
	return result, nil
}

func (store *Store) WriteSettings(ctx context.Context, values map[string]any) error {
	keys := make([]string, 0, len(values))
	encoded := make(map[string][]byte, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			return errors.New("control-plane setting key is required")
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode control-plane setting %s: %w", key, err)
		}
		keys = append(keys, key)
		encoded[key] = raw
	}
	sort.Strings(keys)
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if _, err := transaction.ExecContext(ctx, "DELETE FROM settings"); err != nil {
			return fmt.Errorf("clear control-plane settings: %w", err)
		}
		for _, key := range keys {
			if _, err := transaction.ExecContext(
				ctx,
				"INSERT INTO settings(key, value_json, updated_at) VALUES (?, ?, ?)",
				key,
				string(encoded[key]),
				store.now().Unix(),
			); err != nil {
				return fmt.Errorf("write control-plane setting %s: %w", key, err)
			}
		}
		return nil
	})
}

// UpdateSettings changes only the requested keys in one transaction. Admin
// fine-grained APIs use this instead of a read/replace cycle so unrelated
// settings written by another process cannot be lost.
func (store *Store) UpdateSettings(ctx context.Context, changes map[string]any) error {
	keys := make([]string, 0, len(changes))
	encoded := make(map[string][]byte, len(changes))
	for key, value := range changes {
		key = strings.TrimSpace(key)
		if key == "" {
			return errors.New("control-plane setting key is required")
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode control-plane setting %s: %w", key, err)
		}
		keys = append(keys, key)
		encoded[key] = raw
	}
	sort.Strings(keys)
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		for _, key := range keys {
			if _, err := transaction.ExecContext(ctx, `
                INSERT INTO settings(key, value_json, updated_at) VALUES (?, ?, ?)
                ON CONFLICT(key) DO UPDATE SET
                    value_json = excluded.value_json,
                    updated_at = excluded.updated_at`,
				key,
				string(encoded[key]),
				store.now().Unix(),
			); err != nil {
				return fmt.Errorf("update control-plane setting %s: %w", key, err)
			}
		}
		return nil
	})
}

// ReplaceSettingsAndSecret atomically replaces the complete settings catalog
// and either writes or removes one encrypted secret. Configuration Center uses
// this operation so a process crash cannot leave the default CPA proxy secret
// out of sync with cpa.proxy_enabled or the rendered account configuration.
// A nil secretValue removes the named secret.
func (store *Store) ReplaceSettingsAndSecret(
	ctx context.Context,
	values map[string]any,
	secretName string,
	secretValue *string,
) error {
	return store.ReplaceSettingsAndSecrets(ctx, values, map[string]*string{strings.TrimSpace(secretName): secretValue})
}

// ReplaceSettingsAndSecrets commits the settings and named encrypted secrets in
// one transaction. Omitted secrets are preserved; nil values explicitly delete.
func (store *Store) ReplaceSettingsAndSecrets(ctx context.Context, values map[string]any, secrets map[string]*string) error {
	for name := range secrets {
		if name == "" || name != strings.TrimSpace(name) {
			return errors.New("control-plane secret name is required and must be normalized")
		}
	}
	keys := make([]string, 0, len(values))
	encoded := make(map[string][]byte, len(values))
	for rawKey, value := range values {
		key := strings.TrimSpace(rawKey)
		if key == "" || key != rawKey {
			return errors.New("control-plane setting key is required and must be normalized")
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode control-plane setting %s: %w", key, err)
		}
		keys = append(keys, key)
		encoded[key] = raw
	}
	sort.Strings(keys)

	type encryptedValue struct {
		nonce, ciphertext []byte
		digest            string
	}
	encrypted := make(map[string]encryptedValue, len(secrets))
	for secretName, secretValue := range secrets {
		if secretValue == nil {
			continue
		}
		if *secretValue == "" {
			return fmt.Errorf("control-plane secret %s cannot be empty", secretName)
		}
		nonce, ciphertext, digest, err := store.encryptSecret(ctx, secretName, *secretValue)
		if err != nil {
			return err
		}
		encrypted[secretName] = encryptedValue{nonce, ciphertext, digest}
	}

	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if _, err := transaction.ExecContext(ctx, "DELETE FROM settings"); err != nil {
			return fmt.Errorf("clear control-plane settings: %w", err)
		}
		updatedAt := store.now().Unix()
		for _, key := range keys {
			if _, err := transaction.ExecContext(
				ctx,
				"INSERT INTO settings(key, value_json, updated_at) VALUES (?, ?, ?)",
				key,
				string(encoded[key]),
				updatedAt,
			); err != nil {
				return fmt.Errorf("write control-plane setting %s: %w", key, err)
			}
		}
		for secretName, secretValue := range secrets {
			if secretValue == nil {
				if _, err := transaction.ExecContext(
					ctx,
					"DELETE FROM encrypted_secrets WHERE name = ?",
					secretName,
				); err != nil {
					return fmt.Errorf("delete control-plane secret %s: %w", secretName, err)
				}
				continue
			}
			value := encrypted[secretName]
			if _, err := transaction.ExecContext(ctx, `
            INSERT INTO encrypted_secrets(name, nonce, ciphertext, value_sha256, updated_at)
            VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(name) DO UPDATE SET
                nonce = excluded.nonce,
                ciphertext = excluded.ciphertext,
                value_sha256 = excluded.value_sha256,
                updated_at = excluded.updated_at`,
				secretName, value.nonce, value.ciphertext, value.digest, updatedAt,
			); err != nil {
				return fmt.Errorf("write control-plane secret %s: %w", secretName, err)
			}
		}
		return nil
	})
}

func (store *Store) ReadRuntimeState(ctx context.Context, name string, destination any) (bool, error) {
	return readRuntimeState(ctx, store.db, name, destination)
}

func readRuntimeState(
	ctx context.Context,
	database sqlx.QueryerContext,
	name string,
	destination any,
) (bool, error) {
	var raw string
	err := sqlx.GetContext(ctx, database, &raw, "SELECT payload_json FROM runtime_state WHERE name = ?", name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read control-plane runtime state %s: %w", name, err)
	}
	if err := json.Unmarshal([]byte(raw), destination); err != nil {
		return false, fmt.Errorf("decode control-plane runtime state %s: %w", name, err)
	}
	return true, nil
}

func (store *Store) WriteRuntimeState(ctx context.Context, name string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode control-plane runtime state %s: %w", name, err)
	}
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		_, err := transaction.ExecContext(ctx, `
            INSERT INTO runtime_state(name, payload_json, updated_at)
            VALUES (?, ?, ?)
            ON CONFLICT(name) DO UPDATE SET
                payload_json = excluded.payload_json,
                updated_at = excluded.updated_at`, name, string(raw), store.now().Unix())
		if err != nil {
			return fmt.Errorf("write control-plane runtime state %s: %w", name, err)
		}
		return nil
	})
}

// PatchRuntimeState updates selected top-level JSON fields under the same
// cross-process transaction lock used by other control-plane writes. It keeps
// independent Admin and worker updates from replacing unrelated state fields.
func (store *Store) PatchRuntimeState(ctx context.Context, name string, changes map[string]any) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("control-plane runtime state name is required")
	}
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		payload := make(map[string]any)
		var raw string
		err := transaction.GetContext(
			ctx,
			&raw,
			"SELECT payload_json FROM runtime_state WHERE name = ?",
			name,
		)
		if err == nil {
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				return fmt.Errorf("decode control-plane runtime state %s: %w", name, err)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read control-plane runtime state %s for patch: %w", name, err)
		}
		for key, value := range changes {
			key = strings.TrimSpace(key)
			if key == "" {
				return errors.New("control-plane runtime state field is required")
			}
			payload[key] = value
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode control-plane runtime state %s: %w", name, err)
		}
		if _, err := transaction.ExecContext(ctx, `
            INSERT INTO runtime_state(name, payload_json, updated_at) VALUES (?, ?, ?)
            ON CONFLICT(name) DO UPDATE SET
                payload_json = excluded.payload_json,
                updated_at = excluded.updated_at`,
			name,
			string(encoded),
			store.now().Unix(),
		); err != nil {
			return fmt.Errorf("patch control-plane runtime state %s: %w", name, err)
		}
		return nil
	})
}

func (store *Store) DeleteRuntimeState(ctx context.Context, name string) error {
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if _, err := transaction.ExecContext(ctx, "DELETE FROM runtime_state WHERE name = ?", name); err != nil {
			return fmt.Errorf("delete control-plane runtime state %s: %w", name, err)
		}
		return nil
	})
}

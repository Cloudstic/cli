package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// TxFunc executes fn inside a scoped database transaction.
// The caller is responsible for begin/commit/rollback and any
// session-level configuration (e.g. RLS tenant_id).
type TxFunc func(fn func(ctx context.Context, tx pgx.Tx) error) error

// HybridStore implements ObjectStore by routing chunk data to B2 and
// all metadata objects (node, filemeta, content, snapshot, index) to
// PostgreSQL with write-through to B2 for disaster recovery.
type HybridStore struct {
	db TxFunc
	b2 *B2Store
}

func NewHybridStore(db TxFunc, b2 *B2Store) *HybridStore {
	return &HybridStore{db: db, b2: b2}
}

// B2 returns the underlying B2 store for operations that need direct B2
// access (e.g. zip upload for restore, signed URLs).
func (s *HybridStore) B2() *B2Store { return s.b2 }

// DB returns the TxFunc for direct database access (e.g. reading
// encryption_key_slots that live outside app.objects).
func (s *HybridStore) DB() TxFunc { return s.db }

func (s *HybridStore) Put(key string, data []byte) error {
	if isChunk(key) || isKeySlot(key) {
		return s.b2.Put(key, data)
	}
	if err := s.db(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO app.objects (key, data) VALUES (@key, @data)
			ON CONFLICT (tenant_id, key) DO UPDATE SET data = EXCLUDED.data
		`, pgx.NamedArgs{"key": key, "data": data})
		return err
	}); err != nil {
		return fmt.Errorf("db put %s: %w", key, err)
	}
	_ = s.b2.Put(key, data)
	return nil
}

func (s *HybridStore) Get(key string) ([]byte, error) {
	if isChunk(key) || isKeySlot(key) {
		return s.b2.Get(key)
	}
	var data []byte
	if err := s.db(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT data FROM app.objects WHERE key = @key`,
			pgx.NamedArgs{"key": key},
		).Scan(&data)
	}); err != nil {
		return nil, fmt.Errorf("db get %s: %w", key, err)
	}
	return data, nil
}

func (s *HybridStore) Exists(key string) (bool, error) {
	if isChunk(key) || isKeySlot(key) {
		return s.b2.Exists(key)
	}
	var exists bool
	if err := s.db(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM app.objects WHERE key = @key)`,
			pgx.NamedArgs{"key": key},
		).Scan(&exists)
	}); err != nil {
		return false, err
	}
	return exists, nil
}

func (s *HybridStore) Delete(key string) error {
	if isChunk(key) || isKeySlot(key) {
		return s.b2.Delete(key)
	}
	if err := s.db(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM app.objects WHERE key = @key`,
			pgx.NamedArgs{"key": key},
		)
		return err
	}); err != nil {
		return fmt.Errorf("db delete %s: %w", key, err)
	}
	_ = s.b2.Delete(key)
	return nil
}

func (s *HybridStore) List(prefix string) ([]string, error) {
	if isChunk(prefix) || isKeySlot(prefix) {
		return s.b2.List(prefix)
	}

	var keys []string
	if err := s.db(func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT key FROM app.objects WHERE key LIKE @pattern ORDER BY key`,
			pgx.NamedArgs{"pattern": prefix + "%"},
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			keys = append(keys, k)
		}
		return rows.Err()
	}); err != nil {
		return nil, fmt.Errorf("db list %s: %w", prefix, err)
	}

	if prefix == "" {
		b2Keys, err := s.b2.List("chunk/")
		if err != nil {
			return nil, err
		}
		keys = append(keys, b2Keys...)
	}
	return keys, nil
}

func (s *HybridStore) Size(key string) (int64, error) {
	if isChunk(key) || isKeySlot(key) {
		return s.b2.Size(key)
	}
	var size int64
	if err := s.db(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT length(data) FROM app.objects WHERE key = @key`,
			pgx.NamedArgs{"key": key},
		).Scan(&size)
	}); err != nil {
		return 0, fmt.Errorf("db size %s: %w", key, err)
	}
	return size, nil
}

func (s *HybridStore) TotalSize() (int64, error) {
	return s.b2.TotalSize()
}

func isChunk(key string) bool {
	return strings.HasPrefix(key, "chunk/")
}

// isKeySlot returns true for encryption key slot objects. These are routed
// directly to B2 because the authoritative source is app.encryption_key_slots;
// storing them in app.objects would be redundant.
func isKeySlot(key string) bool {
	return strings.HasPrefix(key, KeySlotPrefix)
}

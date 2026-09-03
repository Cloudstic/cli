package b2

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Backblaze/blazer/b2"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/internal/keyprefix"
)

const b2OpTimeout = 5 * time.Minute

type b2Options struct {
	keyID, appKey string
	prefix        string
	client        *b2.Client
}

// Option configures a B2 store.
type Option func(*b2Options)

// WithPrefix sets a key prefix prepended to every object key.
// Use this to isolate multiple repositories within a single bucket
// (e.g. "prod/" and "staging/").
func WithPrefix(prefix string) Option {
	return func(o *b2Options) {
		o.prefix = keyprefix.Normalize(prefix)
	}
}

// WithClient provides a pre-configured B2 client, skipping internal
// client creation. When set, WithCredentials is ignored.
func WithClient(client *b2.Client) Option {
	return func(o *b2Options) {
		o.client = client
	}
}

// WithCredentials sets the Backblaze application key ID and key used
// to authenticate. Ignored when WithClient is provided.
func WithCredentials(keyID, appKey string) Option {
	return func(o *b2Options) {
		o.keyID = keyID
		o.appKey = appKey
	}
}

// Store implements store.ObjectStore for Backblaze B2.
type Store struct {
	client *b2.Client
	bucket *b2.Bucket
	prefix string
}

// New creates a Store for the given bucket.
// Either WithCredentials or WithClient must be provided.
func New(bucketName string, opts ...Option) (*Store, error) {
	var o b2Options
	for _, opt := range opts {
		opt(&o)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := o.client
	var err error
	if client == nil {
		if o.keyID == "" || o.appKey == "" {
			return nil, fmt.Errorf("B2 credentials (keyID, appKey) or client must be provided")
		}
		client, err = b2.NewClient(ctx, o.keyID, o.appKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create b2 client: %w", err)
		}
	}

	bucket, err := client.Bucket(ctx, bucketName)
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket %s: %w", bucketName, err)
	}
	if bucket == nil {
		return nil, fmt.Errorf("bucket %s not found or accessible", bucketName)
	}

	return &Store{
		client: client,
		bucket: bucket,
		prefix: o.prefix,
	}, nil
}

func (s *Store) key(k string) string {
	return s.prefix + k
}

func (s *Store) opCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, b2OpTimeout)
}

func (s *Store) Put(ctx context.Context, key string, data []byte) error {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	obj := s.bucket.Object(s.key(key))
	w := obj.NewWriter(ctx)
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	obj := s.bucket.Object(s.key(key))
	r := obj.NewReader(ctx)
	defer func() { _ = r.Close() }()

	data, err := io.ReadAll(r)
	if err != nil {
		if b2.IsNotExist(err) {
			return nil, fmt.Errorf("%s: %w", key, store.ErrNotFound)
		}
		return nil, err
	}
	return data, nil
}

// GetRange implements RangeGetter using a B2 ranged download, so a caller
// reading a packfile footer transfers a few hundred bytes rather than the whole
// object.
func (s *Store) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	if offset < 0 || length < 0 {
		return nil, fmt.Errorf("invalid range %d+%d for %s", offset, length, key)
	}
	if length == 0 {
		return []byte{}, nil
	}

	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	obj := s.bucket.Object(s.key(key))
	r := obj.NewRangeReader(ctx, offset, length)
	defer func() { _ = r.Close() }()

	// Short reads mean the object ended early; the caller asked for bytes that
	// are not there, which is an error rather than a truncated slice.
	buf, err := store.ReadExactly(r, length)
	if err != nil {
		return nil, fmt.Errorf("read %s at %d+%d: %w", key, offset, length, err)
	}
	return buf, nil
}

func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	obj := s.bucket.Object(s.key(key))
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		// The blazer library does not expose a typed "not found" error, so we
		// treat any Attrs failure as "does not exist". True network errors
		// will surface on the subsequent Get/Put call.
		return false, nil
	}
	return attrs != nil, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	obj := s.bucket.Object(s.key(key))
	return obj.Delete(ctx)
}

func (s *Store) Size(ctx context.Context, key string) (int64, error) {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	obj := s.bucket.Object(s.key(key))
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		return 0, err
	}
	return attrs.Size, nil
}

// NewWriter returns a streaming writer to the given key in B2.
// The caller must Close the writer to finalize the upload.
func (s *Store) NewWriter(ctx context.Context, key string) io.WriteCloser {
	return s.bucket.Object(s.key(key)).NewWriter(ctx)
}

// SignedURL returns a time-limited download URL for the given key.
func (s *Store) SignedURL(ctx context.Context, key string, validFor time.Duration) (string, error) {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	token, err := s.bucket.AuthToken(ctx, s.key(key), validFor)
	if err != nil {
		return "", fmt.Errorf("generate auth token: %w", err)
	}
	return fmt.Sprintf("%s/file/%s/%s?Authorization=%s",
		s.bucket.BaseURL(), s.bucket.Name(), s.key(key), token), nil
}

func (s *Store) TotalSize(ctx context.Context) (int64, error) {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	var total int64
	var opts []b2.ListOption
	if s.prefix != "" {
		opts = append(opts, b2.ListPrefix(s.prefix))
	}
	cursor := s.bucket.List(ctx, opts...)
	for cursor.Next() {
		attrs, err := cursor.Object().Attrs(ctx)
		if err != nil {
			return 0, err
		}
		total += attrs.Size
	}
	if err := cursor.Err(); err != nil {
		return 0, err
	}
	return total, nil
}

func (s *Store) Flush(ctx context.Context) error {
	return nil
}

// DeletePrefix deletes all objects under the given prefix.
func (s *Store) DeletePrefix(ctx context.Context, prefix string) error {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	fullPrefix := s.key(prefix)

	var opts []b2.ListOption
	if fullPrefix != "" {
		opts = append(opts, b2.ListPrefix(fullPrefix))
	}

	cursor := s.bucket.List(ctx, opts...)
	for cursor.Next() {
		if err := cursor.Object().Delete(ctx); err != nil {
			return fmt.Errorf("delete %s: %w", cursor.Object().Name(), err)
		}
	}
	return cursor.Err()
}

func (s *Store) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	err := s.listObjects(ctx, prefix, func(_ context.Context, key string, _ *b2.Object) error {
		keys = append(keys, key)
		return nil
	})
	return keys, err
}

// ListSized implements store.SizedLister. b2_list_file_names returns each
// file's size with its name, and blazer keeps that on the listed object, so
// Attrs here answers from the listing rather than with a request per key.
func (s *Store) ListSized(ctx context.Context, prefix string, fn func(key string, size int64) error) error {
	return s.listObjects(ctx, prefix, func(ctx context.Context, key string, obj *b2.Object) error {
		attrs, err := obj.Attrs(ctx)
		if err != nil {
			return err
		}
		return fn(key, attrs.Size)
	})
}

// listObjects iterates the bucket under prefix, calling fn with each object's
// key (base prefix stripped) and the listed object, under the listing's own
// context.
func (s *Store) listObjects(ctx context.Context, prefix string, fn func(ctx context.Context, key string, obj *b2.Object) error) error {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	fullPrefix := s.key(prefix)

	var opts []b2.ListOption
	if fullPrefix != "" {
		opts = append(opts, b2.ListPrefix(fullPrefix))
	}

	cursor := s.bucket.List(ctx, opts...)
	for cursor.Next() {
		obj := cursor.Object()
		if err := fn(ctx, strings.TrimPrefix(obj.Name(), s.prefix), obj); err != nil {
			return err
		}
	}

	return cursor.Err()
}

// DeleteAll implements store.BatchDeleter as a loop. Backblaze's native B2 API
// deletes one file version per call; the bulk DeleteObjects form is only
// available through B2's S3-compatible endpoint, which is reached through the
// s3 backend rather than this one.
//
// It is implemented regardless so callers need no fallback branch and per-key
// failures are reported the same way everywhere. blazer has no typed not-found
// error that store.DeleteEach could recognise, so b2.IsNotExist translates it
// here.
func (s *Store) DeleteAll(ctx context.Context, keys []string) error {
	return store.DeleteEach(ctx, keys, func(ctx context.Context, key string) error {
		if err := s.Delete(ctx, key); err != nil && !b2.IsNotExist(err) {
			return err
		}
		return nil
	})
}

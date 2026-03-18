package store

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Backblaze/blazer/b2"
)

const b2OpTimeout = 5 * time.Minute

type b2Options struct {
	keyID, appKey string
	prefix        string
	client        *b2.Client
}

// B2Option configures a B2 store.
type B2Option func(*b2Options)

// WithPrefix sets a key prefix prepended to every object key.
// Use this to isolate multiple repositories within a single bucket
// (e.g. "prod/" and "staging/").
func WithPrefix(prefix string) B2Option {
	return func(o *b2Options) {
		o.prefix = normalizeKeyPrefix(prefix)
	}
}

// WithClient provides a pre-configured B2 client, skipping internal
// client creation. When set, WithCredentials is ignored.
func WithClient(client *b2.Client) B2Option {
	return func(o *b2Options) {
		o.client = client
	}
}

// WithCredentials sets the Backblaze application key ID and key used
// to authenticate. Ignored when WithClient is provided.
func WithCredentials(keyID, appKey string) B2Option {
	return func(o *b2Options) {
		o.keyID = keyID
		o.appKey = appKey
	}
}

// B2Store implements ObjectStore for Backblaze B2.
type B2Store struct {
	client *b2.Client
	bucket *b2.Bucket
	prefix string
}

// NewB2Store creates a B2Store for the given bucket.
// Either WithCredentials or WithClient must be provided.
func NewB2Store(bucketName string, opts ...B2Option) (*B2Store, error) {
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

	return &B2Store{
		client: client,
		bucket: bucket,
		prefix: o.prefix,
	}, nil
}

func (s *B2Store) key(k string) string {
	return s.prefix + k
}

func (s *B2Store) opCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, b2OpTimeout)
}

func (s *B2Store) Put(ctx context.Context, key string, data []byte) error {
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

func (s *B2Store) Get(ctx context.Context, key string) ([]byte, error) {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	obj := s.bucket.Object(s.key(key))
	r := obj.NewReader(ctx)
	defer func() { _ = r.Close() }()

	return io.ReadAll(r)
}

func (s *B2Store) Exists(ctx context.Context, key string) (bool, error) {
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

func (s *B2Store) Delete(ctx context.Context, key string) error {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	obj := s.bucket.Object(s.key(key))
	return obj.Delete(ctx)
}

func (s *B2Store) Size(ctx context.Context, key string) (int64, error) {
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
func (s *B2Store) NewWriter(ctx context.Context, key string) io.WriteCloser {
	return s.bucket.Object(s.key(key)).NewWriter(ctx)
}

// SignedURL returns a time-limited download URL for the given key.
func (s *B2Store) SignedURL(ctx context.Context, key string, validFor time.Duration) (string, error) {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	token, err := s.bucket.AuthToken(ctx, s.key(key), validFor)
	if err != nil {
		return "", fmt.Errorf("generate auth token: %w", err)
	}
	return fmt.Sprintf("%s/file/%s/%s?Authorization=%s",
		s.bucket.BaseURL(), s.bucket.Name(), s.key(key), token), nil
}

func (s *B2Store) TotalSize(ctx context.Context) (int64, error) {
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

func (s *B2Store) Flush(ctx context.Context) error {
	return nil
}

// DeletePrefix deletes all objects under the given prefix.
func (s *B2Store) DeletePrefix(ctx context.Context, prefix string) error {
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

func (s *B2Store) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	fullPrefix := s.key(prefix)

	var keys []string
	var opts []b2.ListOption
	if fullPrefix != "" {
		opts = append(opts, b2.ListPrefix(fullPrefix))
	}

	cursor := s.bucket.List(ctx, opts...)
	for cursor.Next() {
		name := cursor.Object().Name()
		keys = append(keys, strings.TrimPrefix(name, s.prefix))
	}

	if err := cursor.Err(); err != nil {
		return nil, err
	}

	return keys, nil
}

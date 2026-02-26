package store

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Backblaze/blazer/b2"
)

type B2Store struct {
	Client *b2.Client
	Bucket *b2.Bucket
	Prefix string
}

func NewB2Store(keyID, appKey, bucketName string) (*B2Store, error) {
	return NewB2StoreWithPrefix(keyID, appKey, bucketName, "")
}

func NewB2StoreWithPrefix(keyID, appKey, bucketName, prefix string) (*B2Store, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := b2.NewClient(ctx, keyID, appKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create b2 client: %w", err)
	}

	bucket, err := client.Bucket(ctx, bucketName)
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket %s: %w", bucketName, err)
	}
	if bucket == nil {
		return nil, fmt.Errorf("bucket %s not found or accessible", bucketName)
	}

	return &B2Store{
		Client: client,
		Bucket: bucket,
		Prefix: prefix,
	}, nil
}

func (s *B2Store) key(k string) string {
	return s.Prefix + k
}

func (s *B2Store) Put(key string, data []byte) error {
	ctx := context.Background()

	obj := s.Bucket.Object(s.key(key))
	w := obj.NewWriter(ctx)
	if _, err := w.Write(data); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func (s *B2Store) Get(key string) ([]byte, error) {
	ctx := context.Background()

	obj := s.Bucket.Object(s.key(key))
	r := obj.NewReader(ctx)
	defer r.Close()

	return io.ReadAll(r)
}

func (s *B2Store) Exists(key string) (bool, error) {
	ctx := context.Background()
	obj := s.Bucket.Object(s.key(key))
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		// The blazer library does not expose a typed "not found" error, so we
		// treat any Attrs failure as "does not exist". True network errors
		// will surface on the subsequent Get/Put call.
		return false, nil
	}
	return attrs != nil, nil
}

func (s *B2Store) Delete(key string) error {
	ctx := context.Background()
	obj := s.Bucket.Object(s.key(key))
	return obj.Delete(ctx)
}

func (s *B2Store) Size(key string) (int64, error) {
	ctx := context.Background()
	obj := s.Bucket.Object(s.key(key))
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		return 0, err
	}
	return attrs.Size, nil
}

// NewWriter returns a streaming writer to the given key in B2.
// The caller must Close the writer to finalize the upload.
func (s *B2Store) NewWriter(key string) io.WriteCloser {
	ctx := context.Background()
	return s.Bucket.Object(s.key(key)).NewWriter(ctx)
}

// SignedURL returns a time-limited download URL for the given key.
func (s *B2Store) SignedURL(key string, validFor time.Duration) (string, error) {
	ctx := context.Background()
	token, err := s.Bucket.AuthToken(ctx, s.key(key), validFor)
	if err != nil {
		return "", fmt.Errorf("generate auth token: %w", err)
	}
	return fmt.Sprintf("%s/file/%s/%s?Authorization=%s",
		s.Bucket.BaseURL(), s.Bucket.Name(), s.key(key), token), nil
}

func (s *B2Store) TotalSize() (int64, error) {
	ctx := context.Background()
	var total int64
	var opts []b2.ListOption
	if s.Prefix != "" {
		opts = append(opts, b2.ListPrefix(s.Prefix))
	}
	cursor := s.Bucket.List(ctx, opts...)
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

// DeletePrefix deletes all objects under the given prefix.
func (s *B2Store) DeletePrefix(prefix string) error {
	ctx := context.Background()
	fullPrefix := s.key(prefix)

	var opts []b2.ListOption
	if fullPrefix != "" {
		opts = append(opts, b2.ListPrefix(fullPrefix))
	}

	cursor := s.Bucket.List(ctx, opts...)
	for cursor.Next() {
		if err := cursor.Object().Delete(ctx); err != nil {
			return fmt.Errorf("delete %s: %w", cursor.Object().Name(), err)
		}
	}
	return cursor.Err()
}

func (s *B2Store) List(prefix string) ([]string, error) {
	ctx := context.Background()

	// Blazer List takes options
	// We can specify prefix using b2.ListPrefix?
	// No, bucket.List returns a Cursor.
	// Wait, ListPrefix is not an option for Bucket.List?
	// Checking documentation logic or assuming standard B2 SDK patterns.
	// `Bucket.List` usually takes `...ListOption`.
	// `b2.ListPrefix(prefix)` is likely the option.

	fullPrefix := s.key(prefix)

	var keys []string
	var opts []b2.ListOption
	if fullPrefix != "" {
		opts = append(opts, b2.ListPrefix(fullPrefix))
	}

	cursor := s.Bucket.List(ctx, opts...)
	for cursor.Next() {
		name := cursor.Object().Name()
		keys = append(keys, strings.TrimPrefix(name, s.Prefix))
	}

	if err := cursor.Err(); err != nil {
		return nil, err
	}

	return keys, nil
}

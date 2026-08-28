package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/internal/keyprefix"
)

// Store implements store.ObjectStore for Amazon S3 and compatible services.
type Store struct {
	client     *s3.Client
	bucketName string
	prefix     string
}

type s3Options struct {
	endpoint  string
	region    string
	profile   string
	accessKey string
	secretKey string
	prefix    string
	client    *s3.Client
}

// Option configures an S3 store.
type Option func(*s3Options)

// WithEndpoint sets a custom S3-compatible endpoint URL
// (e.g. MinIO, Cloudflare R2). Path-style addressing is
// automatically enabled when an endpoint is set.
func WithEndpoint(endpoint string) Option {
	return func(o *s3Options) {
		o.endpoint = endpoint
	}
}

// WithRegion sets the AWS region for the bucket (e.g. "us-east-1").
func WithRegion(region string) Option {
	return func(o *s3Options) {
		o.region = region
	}
}

// WithProfile sets the AWS shared config profile name used by the SDK
// default credential chain (e.g. profile from ~/.aws/config).
func WithProfile(profile string) Option {
	return func(o *s3Options) {
		o.profile = profile
	}
}

// WithCredentials sets static AWS credentials. When omitted the
// SDK default credential chain is used (env vars, shared config,
// IAM role, etc.).
func WithCredentials(accessKey, secretKey string) Option {
	return func(o *s3Options) {
		o.accessKey = accessKey
		o.secretKey = secretKey
	}
}

// WithPrefix sets a key prefix prepended to every object key.
// Use this to isolate multiple repositories within a single bucket.
func WithPrefix(prefix string) Option {
	return func(o *s3Options) {
		o.prefix = keyprefix.Normalize(prefix)
	}
}

// WithS3Client provides a pre-configured S3 client, skipping
// internal client creation. When set, credential, region, and
// endpoint options are ignored.
func WithS3Client(client *s3.Client) Option {
	return func(o *s3Options) {
		o.client = client
	}
}

// New creates an Store for the given bucket.
// If WithS3Client is not provided, a client is created internally
// using the supplied region, credentials, and endpoint options.
// The internal HTTP transport is tuned for high-concurrency uploads.
const s3Concurrency = 128

func New(ctx context.Context, bucketName string, opts ...Option) (*Store, error) {
	var o s3Options
	for _, opt := range opts {
		opt(&o)
	}

	client := o.client
	if client == nil {
		// Use a high-concurrency HTTP transport for S3. Go's default limits
		// MaxIdleConnsPerHost to 2, which severely throttles parallel uploads.
		httpClient := awshttp.NewBuildableClient().WithTransportOptions(func(t *http.Transport) {
			t.MaxIdleConns = 256
			t.MaxIdleConnsPerHost = s3Concurrency
			t.MaxConnsPerHost = s3Concurrency
		})

		cfgOpts := []func(*config.LoadOptions) error{
			config.WithRegion(o.region),
			config.WithHTTPClient(httpClient),
		}

		if o.profile != "" {
			cfgOpts = append(cfgOpts, config.WithSharedConfigProfile(o.profile))
		}

		if o.accessKey != "" && o.secretKey != "" {
			cfgOpts = append(cfgOpts, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(o.accessKey, o.secretKey, "")))
		}

		cfg, err := config.LoadDefaultConfig(ctx, cfgOpts...)
		if err != nil {
			return nil, fmt.Errorf("failed to load s3 config: %w", err)
		}

		clientOpts := func(co *s3.Options) {
			if o.endpoint != "" {
				co.BaseEndpoint = aws.String(o.endpoint)
				co.UsePathStyle = true // Often needed for custom endpoints like MinIO
			}
		}

		client = s3.NewFromConfig(cfg, clientOpts)
	}

	return &Store{
		client:     client,
		bucketName: bucketName,
		prefix:     o.prefix,
	}, nil
}

// ConcurrencyHint implements ConcurrencyHinter. S3 benefits from highly
// parallel uploads since each PUT is a separate HTTP round-trip.
func (s *Store) ConcurrencyHint() int {
	return s3Concurrency
}

func (s *Store) key(k string) string {
	return s.prefix + k
}

func (s *Store) Put(ctx context.Context, key string, data []byte) error {
	fullKey := s.key(key)
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(fullKey),
		Body:   bytes.NewReader(data),
	})
	return err
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	fullKey := s.key(key)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, fmt.Errorf("%s: %w", key, store.ErrNotFound)
		}
		return nil, err
	}
	defer func() { _ = out.Body.Close() }()

	return io.ReadAll(out.Body)
}

// isS3NotFound reports whether err indicates the requested key or object does
// not exist. GetObject on a missing key typically returns *types.NoSuchKey;
// HeadObject (used by Exists) tends to surface a generic *types.NotFound or an
// APIError coded "NotFound" instead — S3-compatible services (MinIO, R2, ...)
// are not consistent here, so the substring fallback catches the rest.
func isS3NotFound(err error) bool {
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok && apiErr.ErrorCode() == "NotFound" {
		return true
	}
	if _, ok := errors.AsType[*types.NotFound](err); ok {
		return true
	}
	if _, ok := errors.AsType[*types.NoSuchKey](err); ok {
		return true
	}
	return strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "404")
}

// GetRange implements RangeGetter using an HTTP range request, so a caller
// reading a packfile footer transfers a few hundred bytes instead of the whole
// 8 MB object.
func (s *Store) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	if offset < 0 || length < 0 {
		return nil, fmt.Errorf("invalid range %d+%d for %s", offset, length, key)
	}
	if length == 0 {
		return []byte{}, nil
	}

	fullKey := s.key(key)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(fullKey),
		Range:  aws.String(store.HTTPRangeHeader(offset, length)),
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = out.Body.Close() }()

	// A short read means the object ended early: the caller asked for bytes
	// that are not there, which must be an error rather than a truncated slice.
	buf := make([]byte, length)
	if _, err := io.ReadFull(out.Body, buf); err != nil {
		return nil, fmt.Errorf("read %s at %d+%d: %w", key, offset, length, err)
	}
	return buf, nil
}

func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	fullKey := s.key(key)
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(fullKey),
	})

	if err != nil {
		if isS3NotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	fullKey := s.key(key)
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(fullKey),
	})
	return err
}

func (s *Store) Size(ctx context.Context, key string) (int64, error) {
	fullKey := s.key(key)
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return 0, err
	}
	if out.ContentLength == nil {
		return 0, nil
	}
	return *out.ContentLength, nil
}

func (s *Store) TotalSize(ctx context.Context) (int64, error) {
	var total int64
	var continuationToken *string

	prefix := s.prefix
	var prefixPtr *string
	if prefix != "" {
		prefixPtr = aws.String(prefix)
	}

	for {
		out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucketName),
			Prefix:            prefixPtr,
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return 0, err
		}

		for _, obj := range out.Contents {
			if obj.Size != nil {
				total += *obj.Size
			}
		}

		if out.IsTruncated != nil && *out.IsTruncated {
			continuationToken = out.NextContinuationToken
		} else {
			break
		}
	}

	return total, nil
}

func (s *Store) Flush(ctx context.Context) error {
	return nil
}

func (s *Store) List(ctx context.Context, prefix string) ([]string, error) {
	fullPrefix := s.key(prefix)
	var keys []string
	var continuationToken *string

	var prefixPtr *string
	if fullPrefix != "" {
		prefixPtr = aws.String(fullPrefix)
	}

	for {
		out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucketName),
			Prefix:            prefixPtr,
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return nil, err
		}

		for _, obj := range out.Contents {
			if obj.Key != nil {
				// Strip the base prefix before returning the key
				keys = append(keys, strings.TrimPrefix(*obj.Key, s.prefix))
			}
		}

		if out.IsTruncated != nil && *out.IsTruncated {
			continuationToken = out.NextContinuationToken
		} else {
			break
		}
	}

	return keys, nil
}

// s3DeleteBatchSize is the hard limit S3 places on DeleteObjects: at most
// 1,000 keys per request. Longer key lists are split into that many at a time.
const s3DeleteBatchSize = 1000

// errUnaccountedDelete is the failure recorded for a key the response mentioned
// neither as deleted nor as an error. S3 promises a result per key, so this
// should never fire — but "the response did not mention it" is exactly the
// shape of a partial result that must not be read as success, and an
// S3-compatible service is not AWS.
var errUnaccountedDelete = errors.New("not reported in the DeleteObjects response")

// DeleteAll implements store.BatchDeleter over DeleteObjects, taking the sweep
// from one request per object to one per thousand.
//
// The required Content-MD5 (or, on directory buckets, an x-amz-checksum-*
// header) is computed by the SDK: DeleteObjects registers its input checksum
// middleware with RequireChecksum, so it is added whether or not the caller
// asks for a checksum algorithm.
//
// The response is read in verbose mode — the default — which names every key
// deleted as well as every key that failed. That is the point: a key present in
// neither list is unaccounted for rather than gone, and reporting it as a
// failure is what keeps a caller from crediting space it did not reclaim.
func (s *Store) DeleteAll(ctx context.Context, keys []string) error {
	var failures store.DeleteErrors
	for start := 0; start < len(keys); start += s3DeleteBatchSize {
		if err := ctx.Err(); err != nil {
			// The requests already issued stand; only the rest are unconfirmed.
			// Issuing them anyway would return this same error more slowly.
			failures = append(failures, store.UnconfirmedDeletes(keys[start:], err)...)
			break
		}
		end := min(start+s3DeleteBatchSize, len(keys))
		failures = append(failures, s.deleteBatch(ctx, keys[start:end])...)
	}
	if len(failures) > 0 {
		return failures
	}
	return nil
}

// deleteBatch issues one DeleteObjects request and returns the keys it could
// not confirm deleted.
func (s *Store) deleteBatch(ctx context.Context, keys []string) store.DeleteErrors {
	objects := make([]types.ObjectIdentifier, len(keys))
	for i, k := range keys {
		objects[i] = types.ObjectIdentifier{Key: aws.String(s.key(k))}
	}

	out, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(s.bucketName),
		Delete: &types.Delete{Objects: objects},
	})
	if err != nil {
		// The request as a whole failed, so nothing in it is confirmed — but
		// naming the keys keeps the other batches of this call creditable.
		return store.UnconfirmedDeletes(keys, err)
	}

	deleted := make(map[string]bool, len(out.Deleted))
	for _, d := range out.Deleted {
		if d.Key != nil {
			deleted[strings.TrimPrefix(*d.Key, s.prefix)] = true
		}
	}
	reported := make(map[string]error, len(out.Errors))
	for _, e := range out.Errors {
		if e.Key == nil {
			continue
		}
		reported[strings.TrimPrefix(*e.Key, s.prefix)] = deleteObjectsError(e)
	}

	var failures store.DeleteErrors
	for _, k := range keys {
		if deleted[k] {
			continue
		}
		if cause, ok := reported[k]; ok {
			failures = append(failures, store.DeleteError{Key: k, Err: cause})
			continue
		}
		failures = append(failures, store.DeleteError{Key: k, Err: errUnaccountedDelete})
	}
	return failures
}

// deleteObjectsError renders one per-key failure from a DeleteObjects response.
func deleteObjectsError(e types.Error) error {
	code, message := "", ""
	if e.Code != nil {
		code = *e.Code
	}
	if e.Message != nil {
		message = *e.Message
	}
	switch {
	case code != "" && message != "":
		return fmt.Errorf("%s: %s", code, message)
	case code != "":
		return errors.New(code)
	case message != "":
		return errors.New(message)
	}
	return errors.New("delete refused without a reason")
}

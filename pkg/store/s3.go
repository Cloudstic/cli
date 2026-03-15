package store

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
)

// S3Store implements ObjectStore for Amazon S3 and compatible services.
type S3Store struct {
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

// S3Option configures an S3 store.
type S3Option func(*s3Options)

// WithS3Endpoint sets a custom S3-compatible endpoint URL
// (e.g. MinIO, Cloudflare R2). Path-style addressing is
// automatically enabled when an endpoint is set.
func WithS3Endpoint(endpoint string) S3Option {
	return func(o *s3Options) {
		o.endpoint = endpoint
	}
}

// WithS3Region sets the AWS region for the bucket (e.g. "us-east-1").
func WithS3Region(region string) S3Option {
	return func(o *s3Options) {
		o.region = region
	}
}

// WithS3Profile sets the AWS shared config profile name used by the SDK
// default credential chain (e.g. profile from ~/.aws/config).
func WithS3Profile(profile string) S3Option {
	return func(o *s3Options) {
		o.profile = profile
	}
}

// WithS3Credentials sets static AWS credentials. When omitted the
// SDK default credential chain is used (env vars, shared config,
// IAM role, etc.).
func WithS3Credentials(accessKey, secretKey string) S3Option {
	return func(o *s3Options) {
		o.accessKey = accessKey
		o.secretKey = secretKey
	}
}

// WithS3Prefix sets a key prefix prepended to every object key.
// Use this to isolate multiple repositories within a single bucket.
func WithS3Prefix(prefix string) S3Option {
	return func(o *s3Options) {
		o.prefix = prefix
	}
}

// WithS3Client provides a pre-configured S3 client, skipping
// internal client creation. When set, credential, region, and
// endpoint options are ignored.
func WithS3Client(client *s3.Client) S3Option {
	return func(o *s3Options) {
		o.client = client
	}
}

// NewS3Store creates an S3Store for the given bucket.
// If WithS3Client is not provided, a client is created internally
// using the supplied region, credentials, and endpoint options.
// The internal HTTP transport is tuned for high-concurrency uploads.
const s3Concurrency = 128

func NewS3Store(ctx context.Context, bucketName string, opts ...S3Option) (*S3Store, error) {
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

	return &S3Store{
		client:     client,
		bucketName: bucketName,
		prefix:     o.prefix,
	}, nil
}

// ConcurrencyHint implements ConcurrencyHinter. S3 benefits from highly
// parallel uploads since each PUT is a separate HTTP round-trip.
func (s *S3Store) ConcurrencyHint() int {
	return s3Concurrency
}

func (s *S3Store) key(k string) string {
	return s.prefix + k
}

func (s *S3Store) Put(ctx context.Context, key string, data []byte) error {
	fullKey := s.key(key)
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(fullKey),
		Body:   bytes.NewReader(data),
	})
	return err
}

func (s *S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	fullKey := s.key(key)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = out.Body.Close() }()

	return io.ReadAll(out.Body)
}

func (s *S3Store) Exists(ctx context.Context, key string) (bool, error) {
	fullKey := s.key(key)
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(fullKey),
	})

	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NotFound" {
			return false, nil
		}
		// S3 HeadObject can also return generic 404s
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return false, nil
		}
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "404") {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	fullKey := s.key(key)
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(fullKey),
	})
	return err
}

func (s *S3Store) Size(ctx context.Context, key string) (int64, error) {
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

func (s *S3Store) TotalSize(ctx context.Context) (int64, error) {
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

func (s *S3Store) Flush(ctx context.Context) error {
	return nil
}

func (s *S3Store) List(ctx context.Context, prefix string) ([]string, error) {
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

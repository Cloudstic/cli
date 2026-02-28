package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3Store implements ObjectStore for Amazon S3 and compatible services.
type S3Store struct {
	Client     *s3.Client
	BucketName string
	Prefix     string
}

// NewS3Store creates a new S3Store.
// It initializes the AWS SDK config and S3 client.
func NewS3Store(ctx context.Context, endpoint, region, bucketName, accessKey, secretKey, prefix string) (*S3Store, error) {
	opts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
	}

	if accessKey != "" && secretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load s3 config: %w", err)
	}

	clientOpts := func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true // Often needed for custom endpoints like MinIO
		}
	}

	client := s3.NewFromConfig(cfg, clientOpts)

	return &S3Store{
		Client:     client,
		BucketName: bucketName,
		Prefix:     prefix,
	}, nil
}

func (s *S3Store) key(k string) string {
	return s.Prefix + k
}

func (s *S3Store) Put(ctx context.Context, key string, data []byte) error {
	fullKey := s.key(key)
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.BucketName),
		Key:    aws.String(fullKey),
		Body:   bytes.NewReader(data),
	})
	return err
}

func (s *S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	fullKey := s.key(key)
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.BucketName),
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
	_, err := s.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.BucketName),
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
	_, err := s.Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.BucketName),
		Key:    aws.String(fullKey),
	})
	return err
}

func (s *S3Store) Size(ctx context.Context, key string) (int64, error) {
	fullKey := s.key(key)
	out, err := s.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.BucketName),
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

	prefix := s.Prefix
	var prefixPtr *string
	if prefix != "" {
		prefixPtr = aws.String(prefix)
	}

	for {
		out, err := s.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.BucketName),
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

func (s *S3Store) List(ctx context.Context, prefix string) ([]string, error) {
	fullPrefix := s.key(prefix)
	var keys []string
	var continuationToken *string

	var prefixPtr *string
	if fullPrefix != "" {
		prefixPtr = aws.String(fullPrefix)
	}

	for {
		out, err := s.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.BucketName),
			Prefix:            prefixPtr,
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return nil, err
		}

		for _, obj := range out.Contents {
			if obj.Key != nil {
				// Strip the base prefix before returning the key
				keys = append(keys, strings.TrimPrefix(*obj.Key, s.Prefix))
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

// Package s3 provides file storage using AWS S3.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Victoroide/quarry-rag/internal/awsconf"
	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/extractor"
	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Client wraps the AWS S3 SDK for file storage.
type Client struct {
	s3Client  *s3.Client
	presigner *s3.PresignClient
	bucket    string
	owner     *string
}

// ErrBucketUnavailable indicates an inaccessible or missing configured bucket.
var ErrBucketUnavailable = errors.New("s3: bucket unavailable")

// New creates a new S3 client from the service configuration.
func New(ctx context.Context, cfg *config.Config) (*Client, error) {
	awsCfg, err := awsconf.Load(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("s3: load aws config: %w", err)
	}

	slog.InfoContext(ctx, "s3 initialized", "bucket", cfg.S3Bucket, "region", cfg.AWSRegion)
	return newClient(awsCfg, cfg.S3Bucket, cfg.S3ExpectedBucketOwner), nil
}

func newClient(awsCfg aws.Config, bucket, owner string, optFns ...func(*s3.Options)) *Client {
	s3Client := s3.NewFromConfig(awsCfg, optFns...)
	var expectedOwner *string
	if owner != "" {
		expectedOwner = &owner
	}
	return &Client{
		s3Client:  s3Client,
		presigner: s3.NewPresignClient(s3Client),
		bucket:    bucket,
		owner:     expectedOwner,
	}
}

// Upload stores a file in S3 and returns the S3 key.
// Upload callers provide a document ID plus a unique attempt segment.
// Key structure: documents/{docType}/{docID}/{attemptID}/{filename}
func (c *Client) Upload(
	ctx context.Context,
	docID string,
	docType string,
	filename string,
	ft extractor.FileType,
	r io.Reader,
) (string, error) {
	key := fmt.Sprintf("documents/%s/%s/%s", sanitizeKeyComponent(docType, 64, "document"), docID, sanitizeKeyComponent(filename, 200, "file"))
	contentType := extractor.ContentTypeFromFileType(ft)

	_, err := c.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:              &c.bucket,
		Key:                 &key,
		Body:                r,
		ContentType:         &contentType,
		ExpectedBucketOwner: c.owner,
	})
	if err != nil {
		return "", fmt.Errorf("s3: upload %s: %w", key, err)
	}
	slog.DebugContext(ctx, "s3 object uploaded", "key", key, "content_type", contentType)
	return key, nil
}

// PresignGet returns a presigned GET URL valid for 15 minutes.
func (c *Client) PresignGet(ctx context.Context, s3Key string) (string, error) {
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(s3Key)})
	contentType := mime.TypeByExtension(strings.ToLower(path.Ext(s3Key)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req, err := c.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     &c.bucket,
		Key:                        &s3Key,
		ResponseContentDisposition: &disposition,
		ResponseContentType:        &contentType,
	}, s3.WithPresignExpires(15*time.Minute))
	if err != nil {
		return "", fmt.Errorf("s3: presign %s: %w", s3Key, err)
	}
	return req.URL, nil
}

// Delete removes an object from S3 by key.
func (c *Client) Delete(ctx context.Context, s3Key string) error {
	_, err := c.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket:              &c.bucket,
		Key:                 &s3Key,
		ExpectedBucketOwner: c.owner,
	})
	if err != nil {
		return fmt.Errorf("s3: delete %s: %w", s3Key, err)
	}
	slog.DebugContext(ctx, "s3 object deleted", "key", s3Key)
	return nil
}

// Ping verifies bucket access by issuing a HeadBucket call.
func (c *Client) Ping(ctx context.Context) error {
	if err := c.Verify(ctx); err != nil {
		return fmt.Errorf("s3: ping bucket %s: %w", c.bucket, err)
	}
	return nil
}

// Verify checks access to the configured bucket and its expected owner.
func (c *Client) Verify(ctx context.Context) error {
	_, err := c.s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket:              &c.bucket,
		ExpectedBucketOwner: c.owner,
	})
	var responseErr *awshttp.ResponseError
	if errors.As(err, &responseErr) && (responseErr.HTTPStatusCode() == http.StatusForbidden || responseErr.HTTPStatusCode() == http.StatusNotFound) {
		return fmt.Errorf("%w: %w", ErrBucketUnavailable, err)
	}
	return err
}

func sanitizeKeyComponent(value string, limit int, fallback string) string {
	value = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, value)
	trim := func(r rune) bool { return r == '.' || unicode.IsSpace(r) }
	value = strings.TrimFunc(value, trim)
	if len(value) > limit {
		end := limit
		for !utf8.RuneStart(value[end]) {
			end--
		}
		value = strings.TrimFunc(value[:end], trim)
	}
	if value == "" {
		return fallback
	}
	return value
}

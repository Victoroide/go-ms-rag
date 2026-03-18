// Package s3 provides file storage using AWS S3.
package s3

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/Victoroide/go-ms-rag/internal/config"
	"github.com/Victoroide/go-ms-rag/internal/extractor"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Client wraps the AWS S3 SDK for file storage.
type Client struct {
	s3Client  *s3.Client
	presigner *s3.PresignClient
	bucket    string
}

// New creates a new S3 client from the service configuration.
func New(ctx context.Context, cfg *config.Config) (*Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.AWSRegion),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				cfg.AWSAccessKeyID,
				cfg.AWSSecretKey,
				"",
			),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("s3: load aws config: %w", err)
	}

	s3Client := s3.NewFromConfig(awsCfg)
	presigner := s3.NewPresignClient(s3Client)

	log.Printf("s3: initialized (bucket=%s, region=%s)", cfg.S3Bucket, cfg.AWSRegion)
	return &Client{
		s3Client:  s3Client,
		presigner: presigner,
		bucket:    cfg.S3Bucket,
	}, nil
}

// Upload stores a file in S3 and returns the S3 key.
// Key structure: documents/{docType}/{docID}/{filename}
func (c *Client) Upload(
	ctx context.Context,
	docID string,
	docType string,
	filename string,
	ft extractor.FileType,
	r io.Reader,
) (string, error) {
	key := fmt.Sprintf("documents/%s/%s/%s", docType, docID, filename)
	contentType := extractor.ContentTypeFromFileType(ft)

	_, err := c.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &c.bucket,
		Key:         &key,
		Body:        r,
		ContentType: &contentType,
	})
	if err != nil {
		return "", fmt.Errorf("s3: upload %s: %w", key, err)
	}
	log.Printf("s3: uploaded %s (%s)", key, contentType)
	return key, nil
}

// PresignGet returns a presigned GET URL valid for 15 minutes.
func (c *Client) PresignGet(ctx context.Context, s3Key string) (string, error) {
	req, err := c.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &s3Key,
	}, s3.WithPresignExpires(15*time.Minute))
	if err != nil {
		return "", fmt.Errorf("s3: presign %s: %w", s3Key, err)
	}
	return req.URL, nil
}

// Ping verifies bucket access by issuing a HeadBucket call.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: &c.bucket,
	})
	if err != nil {
		return fmt.Errorf("s3: ping bucket %s: %w", c.bucket, err)
	}
	return nil
}

// Package blob stores raw fetched content in MinIO (S3-compatible).
package blob

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Store struct {
	client *minio.Client
	bucket string
}

// New connects to MinIO (retrying) and ensures the bucket exists.
func New(ctx context.Context, endpoint, accessKey, secretKey, bucket string) (*Store, error) {
	var client *minio.Client
	var err error
	for i := 0; i < 30; i++ {
		client, err = minio.New(endpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
			Secure: false,
		})
		if err == nil {
			// Probe by ensuring the bucket.
			if err = ensureBucket(ctx, client, bucket); err == nil {
				return &Store{client: client, bucket: bucket}, nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return nil, err
}

func ensureBucket(ctx context.Context, client *minio.Client, bucket string) error {
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if !exists {
		return client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{})
	}
	return nil
}

// Ping verifies connectivity for readiness checks.
func (s *Store) Ping(ctx context.Context) error {
	_, err := s.client.BucketExists(ctx, s.bucket)
	return err
}

// PutRaw gzip-compresses and stores raw bytes under a content-hash-derived key,
// returning the object key. Keyed by hash → idempotent for identical content.
func (s *Store) PutRaw(ctx context.Context, contentHash []byte, data []byte) (string, error) {
	hexHash := hex.EncodeToString(contentHash)
	now := time.Now().UTC()
	key := fmt.Sprintf("raw/%04d/%02d/%02d/%s.gz", now.Year(), now.Month(), now.Day(), hexHash)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}

	_, err := s.client.PutObject(ctx, s.bucket, key, &buf, int64(buf.Len()),
		minio.PutObjectOptions{ContentType: "application/gzip"})
	if err != nil {
		return "", err
	}
	return key, nil
}

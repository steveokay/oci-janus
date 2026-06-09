package driver

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinIODriver implements Driver using a MinIO (or S3-compatible) backend.
type MinIODriver struct {
	client *minio.Client
	bucket string
}

// NewMinIO creates a MinIODriver. UseSSL should be true in all non-dev environments.
func NewMinIO(endpoint, accessKey, secretKey, bucket, region string, useSSL bool) (*MinIODriver, error) {
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	}
	if region != "" {
		opts.Region = region
	}
	client, err := minio.New(endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}
	return &MinIODriver{client: client, bucket: bucket}, nil
}

func (d *MinIODriver) Ping(ctx context.Context) error {
	_, err := d.client.BucketExists(ctx, d.bucket)
	return err
}

func (d *MinIODriver) PutBlob(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := d.client.PutObject(ctx, d.bucket, key, r, size, minio.PutObjectOptions{
		ContentType:          contentType,
		ServerSideEncryption: nil, // SSE configured at bucket level
	})
	return err
}

func (d *MinIODriver) GetBlob(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	obj, err := d.client.GetObject(ctx, d.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, err
	}
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, 0, err
	}
	return obj, info.Size, nil
}

func (d *MinIODriver) StatBlob(ctx context.Context, key string) (BlobInfo, error) {
	info, err := d.client.StatObject(ctx, d.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return BlobInfo{}, err
	}
	return BlobInfo{
		Key:          info.Key,
		Size:         info.Size,
		ContentType:  info.ContentType,
		LastModified: info.LastModified,
	}, nil
}

func (d *MinIODriver) DeleteBlob(ctx context.Context, key string) error {
	return d.client.RemoveObject(ctx, d.bucket, key, minio.RemoveObjectOptions{})
}

func (d *MinIODriver) BlobExists(ctx context.Context, key string) (bool, error) {
	_, err := d.client.StatObject(ctx, d.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code == "NoSuchKey" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (d *MinIODriver) InitiateMultipart(ctx context.Context, key string) (string, error) {
	// minio-go's Core client exposes NewMultipartUpload.
	core := minio.Core{Client: d.client}
	uploadID, err := core.NewMultipartUpload(ctx, d.bucket, key, minio.PutObjectOptions{})
	return uploadID, err
}

func (d *MinIODriver) UploadPart(ctx context.Context, key, uploadID string, partNum int32, r io.Reader, size int64) (string, error) {
	core := minio.Core{Client: d.client}
	part, err := core.PutObjectPart(ctx, d.bucket, key, uploadID, int(partNum), r, size, minio.PutObjectPartOptions{})
	if err != nil {
		return "", err
	}
	return part.ETag, nil
}

func (d *MinIODriver) CompleteMultipart(ctx context.Context, key, uploadID string, parts []CompletedPart) error {
	minioParts := make([]minio.CompletePart, len(parts))
	for i, p := range parts {
		minioParts[i] = minio.CompletePart{PartNumber: int(p.PartNum), ETag: p.ETag}
	}
	core := minio.Core{Client: d.client}
	_, err := core.CompleteMultipartUpload(ctx, d.bucket, key, uploadID, minioParts, minio.PutObjectOptions{})
	return err
}

func (d *MinIODriver) AbortMultipart(ctx context.Context, key, uploadID string) error {
	core := minio.Core{Client: d.client}
	return core.AbortMultipartUpload(ctx, d.bucket, key, uploadID)
}

func (d *MinIODriver) ListBlobs(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	for obj := range d.client.ListObjects(ctx, d.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		// Skip directory markers
		if !strings.HasSuffix(obj.Key, "/") {
			keys = append(keys, obj.Key)
		}
	}
	return keys, nil
}

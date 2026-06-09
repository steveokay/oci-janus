package driver

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FilesystemDriver implements Driver using the local filesystem.
// FOR DEVELOPMENT / TESTING ONLY. Never use in production.
type FilesystemDriver struct {
	root string
}

// NewFilesystem creates a FilesystemDriver rooted at root (must be an absolute path).
func NewFilesystem(root string) (*FilesystemDriver, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("filesystem root must be an absolute path, got %q", root)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create root dir: %w", err)
	}
	return &FilesystemDriver{root: root}, nil
}

// resolve translates a storage key to an absolute filesystem path and verifies
// it stays within root (path traversal protection).
func (d *FilesystemDriver) resolve(key string) (string, error) {
	clean := filepath.Clean(filepath.Join(d.root, filepath.FromSlash(key)))
	if !strings.HasPrefix(clean, d.root+string(filepath.Separator)) && clean != d.root {
		return "", fmt.Errorf("path traversal detected for key %q", key)
	}
	return clean, nil
}

func (d *FilesystemDriver) Ping(_ context.Context) error {
	_, err := os.Stat(d.root)
	return err
}

func (d *FilesystemDriver) PutBlob(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	path, err := d.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	// Write to temp file then rename for atomicity.
	tmp := path + ".tmp"
	f, err := os.Create(tmp) //nolint:gosec
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write blob: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp file: %w", err)
	}
	return os.Rename(tmp, path)
}

func (d *FilesystemDriver) GetBlob(_ context.Context, key string) (io.ReadCloser, int64, error) {
	path, err := d.resolve(key)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

func (d *FilesystemDriver) StatBlob(_ context.Context, key string) (BlobInfo, error) {
	path, err := d.resolve(key)
	if err != nil {
		return BlobInfo{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return BlobInfo{}, err
	}
	return BlobInfo{
		Key:          key,
		Size:         info.Size(),
		ContentType:  "application/octet-stream",
		LastModified: info.ModTime(),
	}, nil
}

func (d *FilesystemDriver) DeleteBlob(_ context.Context, key string) error {
	path, err := d.resolve(key)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func (d *FilesystemDriver) BlobExists(_ context.Context, key string) (bool, error) {
	path, err := d.resolve(key)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// Multipart operations: filesystem driver assembles parts in a temp directory
// and renames on completion.

func (d *FilesystemDriver) InitiateMultipart(_ context.Context, key string) (string, error) {
	// Use key hash as upload ID to keep it deterministic and path-safe.
	uploadID := fmt.Sprintf("%x", time.Now().UnixNano())
	dir := filepath.Join(d.root, ".multipart", uploadID)
	return uploadID, os.MkdirAll(dir, 0o755)
}

func (d *FilesystemDriver) UploadPart(_ context.Context, _, uploadID string, partNum int32, r io.Reader, _ int64) (string, error) {
	partPath := filepath.Join(d.root, ".multipart", uploadID, fmt.Sprintf("%05d", partNum))
	f, err := os.Create(partPath) //nolint:gosec
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return "", err
	}
	return fmt.Sprintf("etag-%d", partNum), nil
}

func (d *FilesystemDriver) CompleteMultipart(ctx context.Context, key, uploadID string, parts []CompletedPart) error {
	destPath, err := d.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}

	tmp := destPath + ".tmp"
	dst, err := os.Create(tmp) //nolint:gosec
	if err != nil {
		return err
	}
	defer dst.Close()

	for _, p := range parts {
		partPath := filepath.Join(d.root, ".multipart", uploadID, fmt.Sprintf("%05d", p.PartNum))
		src, err := os.Open(partPath) //nolint:gosec
		if err != nil {
			os.Remove(tmp)
			return err
		}
		_, copyErr := io.Copy(dst, src)
		src.Close()
		if copyErr != nil {
			os.Remove(tmp)
			return copyErr
		}
	}
	dst.Close()

	if err := os.Rename(tmp, destPath); err != nil {
		return err
	}
	return d.AbortMultipart(ctx, key, uploadID)
}

func (d *FilesystemDriver) AbortMultipart(_ context.Context, _, uploadID string) error {
	dir := filepath.Join(d.root, ".multipart", uploadID)
	return os.RemoveAll(dir)
}

func (d *FilesystemDriver) ListBlobs(_ context.Context, prefix string) ([]string, error) {
	searchRoot, err := d.resolve(prefix)
	if err != nil {
		return nil, err
	}

	var keys []string
	err = filepath.Walk(searchRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() || strings.Contains(path, ".multipart") {
			return nil
		}
		rel, err := filepath.Rel(d.root, path)
		if err != nil {
			return err
		}
		keys = append(keys, filepath.ToSlash(rel))
		return nil
	})
	return keys, err
}

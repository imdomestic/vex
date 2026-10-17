package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/imdomestic/vex/controller/internal/filehash"
)

type ObjectInfo struct {
	Size int64
}

type Store interface {
	Put(context.Context, string, string) error
	Verify(context.Context, string, string, int64) error
	Get(context.Context, string, string) error
	Stat(context.Context, string) (ObjectInfo, error)
}

type LocalStore struct {
	Directory string
}

func (s LocalStore) root(key string) (*os.Root, error) {
	if !safeRelative(key) || !filepath.IsAbs(s.Directory) {
		return nil, fmt.Errorf("archive root and relative key are required")
	}
	return os.OpenRoot(s.Directory)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(buffer)
}

func (s LocalStore) Put(ctx context.Context, key, source string) error {
	root, err := s.root(key)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	existing, err := root.Open(key)
	if err == nil {
		defer existing.Close()
		original, err := filehash.Reader(contextReader{ctx, existing})
		if err != nil {
			return err
		}
		incoming, err := filehash.Reader(contextReader{ctx, file})
		if err != nil {
			return err
		}
		if original != incoming {
			return fmt.Errorf("archive key already contains different bytes")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = root.MkdirAll(path.Dir(key), 0750); err != nil {
		return err
	}
	partial := key + "." + uuid.NewString() + ".partial"
	output, err := root.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return err
	}
	defer root.Remove(partial)
	_, copyErr := io.Copy(output, contextReader{ctx, file})
	syncErr := output.Sync()
	closeErr := output.Close()
	if err = errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	return root.Rename(partial, key)
}

func (s LocalStore) Verify(ctx context.Context, key, digest string, size int64) error {
	root, err := s.root(key)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.Open(key)
	if err != nil {
		return err
	}
	defer file.Close()
	hashes, err := filehash.Reader(contextReader{ctx, file})
	if err != nil {
		return err
	}
	if hashes.SHA256 != digest || hashes.Size != size {
		return fmt.Errorf("archive object checksum or size mismatch")
	}
	return nil
}

func (s LocalStore) Get(ctx context.Context, key, destination string) error {
	root, err := s.root(key)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.Open(key)
	if err != nil {
		return err
	}
	defer file.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, contextReader{ctx, file})
	syncErr := output.Sync()
	closeErr := output.Close()
	return errors.Join(copyErr, syncErr, closeErr)
}

func (s LocalStore) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	root, err := s.root(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	defer root.Close()
	info, err := root.Stat(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if !info.Mode().IsRegular() {
		return ObjectInfo{}, fmt.Errorf("archive object is not a regular file")
	}
	return ObjectInfo{Size: info.Size()}, nil
}

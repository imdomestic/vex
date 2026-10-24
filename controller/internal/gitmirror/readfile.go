package gitmirror

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

// ReadFile 只读取指定 commit 中的常规文件，缺失文件返回 fs.ErrNotExist。
func (m *Mirror) ReadFile(ctx context.Context, sha, filename string, maxBytes int64) ([]byte, error) {
	if !fs.ValidPath(filename) || path.Clean(filename) != filename || strings.ContainsAny(filename, "\\\x00\r\n") || maxBytes < 1 || maxBytes > 16*1024*1024 {
		return nil, fmt.Errorf("invalid git file path or size limit")
	}
	if err := m.mu.Lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	if err := m.verifyCommit(ctx, sha); err != nil {
		return nil, err
	}
	mode, err := m.git(ctx, "ls-tree", "--format=%(objectmode)", sha, "--", filename)
	if err != nil {
		return nil, err
	}
	if mode == "" {
		return nil, fmt.Errorf("commit file is absent: %w", fs.ErrNotExist)
	}
	if strings.TrimSpace(mode) != "100644" && strings.TrimSpace(mode) != "100755" {
		return nil, fmt.Errorf("commit file must be a regular blob")
	}
	object := sha + ":" + filename
	sizeText, err := m.git(ctx, "cat-file", "-s", object)
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
	if err != nil || size < 0 || size > maxBytes {
		return nil, fmt.Errorf("commit file exceeds its size limit")
	}
	data, err := m.git(ctx, "cat-file", "blob", object)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, fmt.Errorf("commit blob size changed")
	}
	return []byte(data), nil
}

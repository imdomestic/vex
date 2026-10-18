package gitmirror

import (
	"context"
	"fmt"
	"strings"
)

func (m *Mirror) ResolveCommitPrefix(ctx context.Context, prefix string) (string, error) {
	if len(prefix) < 12 || len(prefix) > 40 || strings.Trim(prefix, "0123456789abcdef") != "" {
		return "", fmt.Errorf("commit prefix must contain 12 to 40 lowercase hexadecimal characters")
	}
	if err := m.mu.Lock(ctx); err != nil {
		return "", err
	}
	defer m.mu.Unlock()
	output, err := m.git(ctx, "rev-parse", "--verify", "--end-of-options", prefix+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("recorded build commit prefix is missing or ambiguous in the local mirror")
	}
	sha := strings.TrimSpace(output)
	if !fullSHA.MatchString(sha) || !strings.HasPrefix(sha, prefix) {
		return "", fmt.Errorf("resolved commit does not match the recorded prefix")
	}
	return sha, nil
}

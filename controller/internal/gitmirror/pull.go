package gitmirror

import (
	"context"
	"fmt"
	"strconv"
)

func (m *Mirror) FetchPull(ctx context.Context, number int, sha string) error {
	if number <= 0 || !fullSHA.MatchString(sha) {
		return fmt.Errorf("invalid pull request reference")
	}
	if err := m.refreshMu.Lock(ctx); err != nil {
		return err
	}
	defer m.refreshMu.Unlock()
	ref := "refs/pull/" + strconv.Itoa(number) + "/head"
	if _, err := m.git(ctx, "fetch", "--no-tags", "origin", "+"+ref+":"+ref); err != nil {
		return err
	}
	return m.VerifyCommit(ctx, sha)
}

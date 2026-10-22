package gitmirror

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/imdomestic/vex/controller/internal/model"
)

func TestRewrittenHistoryOnlyBlocksNewWorkOnRemovedCommits(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	git(t, root, "init", "--bare", "--initial-branch=main", remote)
	git(t, root, "clone", remote, work)
	base := commit(t, work, "base.txt", "保留的基础提交")
	git(t, work, "push", "origin", "HEAD:refs/heads/main")
	git(t, work, "checkout", "-b", "feat/rewrite")
	removed := commit(t, work, "removed.txt", "将被强制推送删除的提交")
	git(t, work, "push", "origin", "feat/rewrite")
	mirror, err := New(remote, filepath.Join(root, "mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	reachable, err := mirror.Reachable(ctx)
	if err != nil || !reachable[base] || !reachable[removed] {
		t.Fatalf("reachable commits before rewrite: %v %v", reachable, err)
	}
	info, err := mirror.CommitInfo(ctx, removed)
	if err != nil || info.SHA != removed || info.Subject != "将被强制推送删除的提交" || info.Author != "Vex Integration" || info.Time.IsZero() {
		t.Fatalf("commit metadata: %+v %v", info, err)
	}
	git(t, work, "push", "--force", "origin", base+":refs/heads/feat/rewrite")
	if err = mirror.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	reachable, err = mirror.Reachable(ctx)
	if err != nil || !reachable[base] || reachable[removed] {
		t.Fatalf("reachable commits after rewrite: %v %v", reachable, err)
	}
	var failure *model.Error
	if err = mirror.VerifyCommit(ctx, removed); !errors.As(err, &failure) || failure.Code != "commit_unavailable" {
		t.Fatalf("new work on an unreachable commit: %v", err)
	}
	if err = mirror.VerifyCommit(ctx, base); err != nil {
		t.Fatal(err)
	}
	// 被丢弃的对象在 gc 之前仍可读取，补录依赖这一点；gc 之后读取明确失败。
	if _, err = mirror.CommitInfo(ctx, removed); err != nil {
		t.Fatal("dangling commit unreadable before gc", err)
	}
	git(t, root, "--git-dir="+filepath.Join(root, "mirror.git"), "gc", "--prune=now")
	if _, err = mirror.CommitInfo(ctx, removed); !errors.As(err, &failure) || failure.Code != "commit_unavailable" {
		t.Fatalf("commit metadata after gc: %v", err)
	}
	if _, err = mirror.ReadFile(ctx, removed, "removed.txt", 1024); !errors.As(err, &failure) || failure.Code != "commit_unavailable" {
		t.Fatalf("file read after gc: %v", err)
	}
	commits, err := mirror.Commits(ctx, "feat/rewrite", "", 10)
	if err != nil || len(commits) != 1 || commits[0].SHA != base {
		t.Fatalf("branch history after rewrite: %+v %v", commits, err)
	}
}

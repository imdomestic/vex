package gitmirror

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "user.name=Vex Integration", "-c", "user.email=vex@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestUnavailableRemotePreservesCachedHistoryAndEmptyMirror(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git(t, root, "init", "--initial-branch=main", source)
	sha := commit(t, source, "data.txt", "cached source")
	cached, err := New(source, filepath.Join(root, "cached.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = cached.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err = cached.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(source, source+"-offline"); err != nil {
		t.Fatal(err)
	}
	if err = cached.Refresh(ctx); err == nil {
		t.Fatal("unavailable remote accepted")
	}
	if err = cached.VerifyCommit(ctx, sha); err != nil {
		t.Fatal(err)
	}
	refs, err := cached.Branches(ctx)
	if err != nil || len(refs) != 1 || refs[0].Head != sha {
		t.Fatalf("cached refs: %+v %v", refs, err)
	}
	empty, err := New("http://127.0.0.1:1/unreachable.git", filepath.Join(root, "empty.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = empty.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err = empty.Refresh(ctx); err == nil {
		t.Fatal("unreachable remote accepted")
	}
	refs, err = empty.Branches(ctx)
	if err != nil || len(refs) != 0 {
		t.Fatalf("empty mirror: %+v %v", refs, err)
	}
	var failure *model.Error
	if err = empty.VerifyCommit(ctx, sha); !errors.As(err, &failure) || failure.Code != "not_found" || !strings.Contains(failure.Message, "remote refresh") {
		t.Fatalf("missing source error: %v", err)
	}
}

func commit(t *testing.T, repo, filename, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, filename), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "--", filename)
	git(t, repo, "commit", "-m", content)
	return git(t, repo, "rev-parse", "HEAD")
}

func TestMirrorRealHistoryCheckoutAndPrune(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git(t, root, "init", "--initial-branch=main", source)
	initial := commit(t, source, "base.txt", "初始记录")
	git(t, source, "checkout", "-b", "feat/perks")
	feature := commit(t, source, "feature.txt", "属性内容")
	git(t, source, "checkout", "main")
	main := commit(t, source, "main.txt", "主线内容")
	git(t, source, "merge", "--no-ff", "feat/perks", "-m", "合并属性分支")
	merged := git(t, source, "rev-parse", "HEAD")
	git(t, source, "tag", "-a", "v-test", "-m", "真实标签", merged)
	mirror, err := New(source, filepath.Join(root, "mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	refs, err := mirror.Branches(ctx)
	if err != nil || len(refs) != 2 || refs[0].Name != "feat/perks" || refs[1].Head != merged {
		t.Fatalf("branches: %+v, %v", refs, err)
	}
	first, err := mirror.Commits(ctx, "main", "", 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	if first[0].SHA != merged || len(first[0].Parents) != 2 || first[0].Parents[0] != main || first[0].Parents[1] != feature {
		t.Fatalf("merge history lost: %+v", first[0])
	}
	if first[0].Subject != "合并属性分支" || first[0].Author != "Vex Integration" || strings.Join(first[0].Refs, ",") != "main,v-test" {
		t.Fatalf("commit metadata: %+v", first[0])
	}
	second, err := mirror.Commits(ctx, "main", first[1].SHA, 2)
	if err != nil || len(second) != 2 || second[1].SHA != initial {
		t.Fatalf("second page: %+v, %v", second, err)
	}
	seen := map[string]bool{}
	for _, item := range append(first, second...) {
		if seen[item.SHA] {
			t.Fatalf("duplicate paginated commit %s", item.SHA)
		}
		seen[item.SHA] = true
	}
	checkout := filepath.Join(root, "work", "build")
	if err = mirror.Checkout(ctx, feature, checkout); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(checkout, "feature.txt"))
	if err != nil || string(contents) != "属性内容" {
		t.Fatalf("checkout contents: %q, %v", contents, err)
	}
	if err = mirror.RemoveCheckout(ctx, source); err == nil {
		t.Fatal("accepted a worktree owned by another repository")
	}
	if err = mirror.RemoveCheckout(ctx, checkout); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(checkout); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree remains: %v", err)
	}
	git(t, source, "branch", "-d", "feat/perks")
	if err = mirror.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	refs, err = mirror.Branches(ctx)
	if err != nil || len(refs) != 1 || refs[0].Name != "main" {
		t.Fatalf("deleted branch not pruned: %+v, %v", refs, err)
	}
	if git(t, source, "status", "--porcelain=v1") != "" {
		t.Fatal("source repository changed during mirror operations")
	}
}

func TestMirrorRejectsInvalidAndAbsentCommits(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git(t, root, "init", "--initial-branch=main", source)
	sha := commit(t, source, "data.txt", "actual data")
	git(t, source, "tag", "-a", "release", "-m", "真实标签", sha)
	tagSHA := git(t, source, "rev-parse", "refs/tags/release")
	mirror, err := New(source, filepath.Join(root, "mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = mirror.VerifyCommit(ctx, sha); err != nil {
		t.Fatal(err)
	}
	for input, code := range map[string]string{"--all": "invalid_request", strings.Repeat("0", 40): "not_found", tagSHA: "not_found"} {
		var failure *model.Error
		if err = mirror.VerifyCommit(ctx, input); !errors.As(err, &failure) || failure.Code != code {
			t.Fatalf("verification %q: %v", input, err)
		}
	}
	if _, err = mirror.Commits(ctx, "missing", "", 50); err == nil {
		t.Fatal("missing branch accepted")
	}
	if _, err = mirror.Commits(ctx, "main", strings.Repeat("0", 40), 50); err == nil {
		t.Fatal("missing cursor accepted")
	}
	if _, err = mirror.Commits(ctx, "main", "", 0); err == nil {
		t.Fatal("invalid page limit accepted")
	}
}

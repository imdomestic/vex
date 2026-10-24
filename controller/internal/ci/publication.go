package ci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

type publication struct {
	CommentID   int64     `json:"commentId"`
	CheckID     int64     `json:"checkId"`
	HeadSHA     string    `json:"headSha"`
	Fingerprint string    `json:"fingerprint"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type previewRow struct {
	loader   string
	build    *model.Build
	room     *model.Room
	snapshot string
}

type previewView struct {
	pull       model.Pull
	rows       []previewRow
	status     string
	conclusion string
	validation string
}

func DeepLink(base, project, room string) string {
	value := url.URL{Scheme: "ruri", Host: "vex"}
	query := url.Values{"server": {strings.TrimRight(base, "/")}, "project": {project}, "room": {room}}
	value.RawQuery = query.Encode()
	return value.String()
}

func (m *Manager) preview(ctx context.Context, pull model.Pull, builds []model.Build) (previewView, error) {
	view := previewView{pull: pull, status: "queued"}
	runs, err := store.All[runRecord](ctx, m.db, "ci_run")
	if err != nil {
		return view, err
	}
	var latest *runRecord
	for i := range runs {
		if runs[i].HeadSHA == pull.HeadSHA && strings.EqualFold(runs[i].HeadRepository, pull.HeadRepository) && (latest == nil || runs[i].UpdatedAt.After(latest.UpdatedAt)) {
			latest = &runs[i]
		}
	}
	if latest != nil {
		view.validation = latest.Status
	}
	snapshots, err := store.All[model.Snapshot](ctx, m.db, "snapshot")
	if err != nil {
		return view, err
	}
	loaderSet := map[string]bool{}
	for loader := range pull.Rooms {
		loaderSet[loader] = true
	}
	for _, value := range builds {
		matches, err := m.buildMatchesPull(ctx, value, pull)
		if err != nil {
			return view, err
		}
		if matches {
			loaderSet[value.Loader] = true
		}
	}
	if len(loaderSet) == 0 {
		for _, loader := range m.cfg.EnabledLoaders() {
			loaderSet[loader] = true
		}
	}
	loaders := make([]string, 0, len(loaderSet))
	for loader := range loaderSet {
		loaders = append(loaders, loader)
	}
	sort.Strings(loaders)
	for _, loader := range loaders {
		row := previewRow{loader: loader}
		for i := range builds {
			if builds[i].Loader != loader {
				continue
			}
			matches, err := m.buildMatchesPull(ctx, builds[i], pull)
			if err != nil {
				return view, err
			}
			if matches {
				row.build = &builds[i]
			}
		}
		if id := pull.Rooms[loader]; id != "" {
			room, err := m.rooms.Get(ctx, id)
			if err != nil {
				return view, err
			}
			row.room = &room
			generation := int64(-1)
			for _, snapshot := range snapshots {
				if snapshot.RoomID == room.ID && snapshot.RoomGeneration > generation {
					generation, row.snapshot = snapshot.RoomGeneration, snapshot.ID
				}
			}
		}
		view.rows = append(view.rows, row)
	}
	view.status, view.conclusion = previewCheck(view, m.cfg.Wake.Enabled)
	return view, nil
}

func previewCheck(view previewView, wakeEnabled bool) (string, string) {
	if view.pull.State == "closed" {
		for _, row := range view.rows {
			if row.room != nil && row.room.State != "closed" {
				return "in_progress", ""
			}
		}
		return "completed", "neutral"
	}
	if view.pull.Fork && !view.pull.Approved {
		return "queued", ""
	}
	if view.validation == "failed" {
		return "completed", "failure"
	}
	if view.validation == "validating" {
		return "in_progress", ""
	}
	ready := len(view.rows) > 0
	accepted := view.validation == "ready"
	for _, row := range view.rows {
		if row.build != nil {
			accepted = true
			if row.build.Status == "failed" {
				return "completed", "failure"
			}
		}
		if row.room != nil && row.room.State == "failed" {
			return "completed", "failure"
		}
		if row.build == nil || row.build.Status != "ready" || row.room == nil || !(row.room.State == "ready" && row.room.BuildID == row.build.ID) && !(row.room.State == "stopped" && wakeEnabled) {
			ready = false
		}
	}
	if ready {
		return "completed", "success"
	}
	if accepted {
		return "in_progress", ""
	}
	return "queued", ""
}

func renderPreview(project, access, language string, view previewView) string {
	var body strings.Builder
	marker := "<!-- vex:preview project=" + project + " -->"
	zh := language == "zh-CN"
	if zh {
		fmt.Fprintf(&body, "%s\n### Vex 预览\n\n提交：`%s` · 访问范围：**%s**\n", marker, view.pull.HeadSHA, access)
		if view.pull.State == "closed" {
			body.WriteString("\n**已关闭**\n")
		}
		if view.pull.Fork && !view.pull.Approved && view.pull.State == "open" {
			body.WriteString("\n等待批准。拥有仓库写权限的维护者可在当前提交之后评论 `/vex preview`，或者添加 `vex-preview` 标签。\n")
		}
	} else {
		fmt.Fprintf(&body, "%s\n### Vex preview\n\nCommit: `%s` · Access: **%s**\n", marker, view.pull.HeadSHA, access)
		if view.pull.State == "closed" {
			body.WriteString("\n**Closed**\n")
		}
		if view.pull.Fork && !view.pull.Approved && view.pull.State == "open" {
			body.WriteString("\nWaiting for approval. A maintainer with repository write permission may comment `/vex preview` or add the `vex-preview` label after the current head commit.\n")
		}
	}
	if view.validation == "failed" {
		if zh {
			body.WriteString("\n**产物校验失败**：下载、完整性或提交清单检查未通过。\n")
		} else {
			body.WriteString("\n**Artifact validation failed**: download, integrity or authoritative commit manifest verification did not pass.\n")
		}
	} else if view.validation == "validating" {
		if zh {
			body.WriteString("\n正在校验 CI 产物。\n")
		} else {
			body.WriteString("\nValidating the accepted CI artifact.\n")
		}
	}
	for _, row := range view.rows {
		fmt.Fprintf(&body, "\n- **%s**", row.loader)
		if row.build == nil {
			if zh {
				body.WriteString(" · 等待 CI 产物")
			} else {
				body.WriteString(" · waiting for CI artifact")
			}
		} else {
			fmt.Fprintf(&body, " · Build `%s` / attempt %d / **%s**", row.build.ID, row.build.Attempt, row.build.Status)
		}
		if row.room != nil {
			fmt.Fprintf(&body, " · `%s:%d` · Room **%s**, generation %d, build `%s`", row.room.Host, row.room.Port, row.room.State, row.room.Generation, row.room.BuildID)
		}
		if row.snapshot != "" {
			if zh {
				fmt.Fprintf(&body, " · 最后快照 `%s`", row.snapshot)
			} else {
				fmt.Fprintf(&body, " · last snapshot `%s`", row.snapshot)
			}
		}
		body.WriteByte('\n')
	}
	return body.String()
}

func (m *Manager) publish(ctx context.Context, client *github.Client, owner, repo string, pull model.Pull, builds []model.Build) error {
	key := strconv.Itoa(pull.Number)
	var previous publication
	if err := m.db.Get(ctx, "ci_publication", key, &previous); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	view, err := m.preview(ctx, pull, builds)
	if err != nil {
		return err
	}
	stable := renderPreview(m.cfg.ID, m.cfg.CI.PreviewAccess, m.cfg.CI.CommentLanguage, view)
	digest := sha256.Sum256([]byte(stable + view.status + view.conclusion))
	fingerprint := hex.EncodeToString(digest[:])
	ttl := config.Duration(m.cfg.CI.PackLinkTTL)
	if ttl <= 0 {
		return fmt.Errorf("PR publication requires a positive pack link TTL")
	}
	now := time.Now().UTC()
	if previous.Fingerprint == fingerprint && (pull.State == "closed" || previous.ExpiresAt.After(now.Add(min(ttl/4, 6*time.Hour)))) {
		return nil
	}
	expires := now.Add(ttl)
	var body strings.Builder
	body.WriteString(stable)
	for _, row := range view.rows {
		if row.room == nil || row.build == nil || row.build.Status != "ready" || pull.State != "open" || !pull.Approved {
			continue
		}
		path := "/v2/projects/" + m.cfg.ID + "/rooms/" + row.room.ID + "/pack.mrpack"
		download, err := m.auth.SignURL(m.cfg.Server.PublicBaseURL, path, expires)
		if err != nil {
			return err
		}
		fmt.Fprintf(&body, "\n[%s mrpack](%s) · [Ruri](%s)\n", row.loader, download, DeepLink(m.cfg.Server.PublicBaseURL, m.cfg.ID, row.room.ID))
	}
	if m.cfg.CI.CommentLanguage == "zh-CN" {
		fmt.Fprintf(&body, "\n更新时间：%s。下载链接有效至 %s；连接房间需要能够访问此 Vex 服务器的网络。\n", now.Format(time.RFC3339), expires.Format(time.RFC3339))
	} else {
		fmt.Fprintf(&body, "\nUpdated: %s. Download links expire at %s; server addresses require access to this Vex server's network.\n", now.Format(time.RFC3339), expires.Format(time.RFC3339))
	}
	contents := body.String()
	marker := "<!-- vex:preview project=" + m.cfg.ID + " -->"
	if previous.CommentID == 0 {
		for comment, err := range client.Issues.ListCommentsIter(ctx, owner, repo, pull.Number, &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}) {
			if err != nil {
				return apiFailure("find preview comment", err)
			}
			if comment.GetUser().GetLogin() == m.app.BotLogin() && strings.HasPrefix(comment.GetBody(), marker+"\n") {
				previous.CommentID = comment.GetID()
				break
			}
		}
	}
	if previous.CommentID == 0 {
		comment, _, err := client.Issues.CreateComment(ctx, owner, repo, pull.Number, &github.IssueComment{Body: &contents})
		if err != nil {
			return apiFailure("create preview comment", err)
		}
		previous.CommentID = comment.GetID()
		if err = m.db.Put(ctx, "ci_publication", key, previous); err != nil {
			return err
		}
	} else if _, _, err = client.Issues.EditComment(ctx, owner, repo, previous.CommentID, &github.IssueComment{Body: &contents}); err != nil {
		return apiFailure("update preview comment", err)
	}
	name := "vex/preview"
	externalID := m.cfg.ID + "/pr/" + key
	if previous.CheckID == 0 || previous.HeadSHA != pull.HeadSHA {
		previous.CheckID = 0
		options := &github.ListCheckRunsOptions{CheckName: &name, AppID: &m.app.ID, Filter: github.Ptr("all"), ListOptions: github.ListOptions{PerPage: 100}}
		for {
			found, response, err := client.Checks.ListCheckRunsForRef(ctx, owner, repo, pull.HeadSHA, options)
			if err != nil {
				return apiFailure("find preview check", err)
			}
			for _, check := range found.CheckRuns {
				if check.GetExternalID() == externalID {
					previous.CheckID = check.GetID()
					break
				}
			}
			if previous.CheckID != 0 || response.NextPage == 0 {
				break
			}
			options.Page = response.NextPage
		}
	}
	output := &github.CheckRunOutput{Title: github.Ptr("Vex preview"), Summary: &contents}
	var conclusion *string
	var completed *github.Timestamp
	if view.status == "completed" {
		conclusion, completed = &view.conclusion, &github.Timestamp{Time: now}
	}
	if previous.CheckID == 0 {
		check, _, err := client.Checks.CreateCheckRun(ctx, owner, repo, github.CreateCheckRunOptions{Name: name, HeadSHA: pull.HeadSHA, ExternalID: &externalID, Status: &view.status, Conclusion: conclusion, CompletedAt: completed, Output: output})
		if err != nil {
			return apiFailure("create preview check", err)
		}
		previous.CheckID = check.GetID()
	} else if _, _, err = client.Checks.UpdateCheckRun(ctx, owner, repo, previous.CheckID, github.UpdateCheckRunOptions{Name: name, Status: &view.status, Conclusion: conclusion, CompletedAt: completed, Output: output}); err != nil {
		return apiFailure("update preview check", err)
	}
	previous.HeadSHA, previous.Fingerprint, previous.ExpiresAt = pull.HeadSHA, fingerprint, expires
	return m.db.Put(ctx, "ci_publication", key, previous)
}

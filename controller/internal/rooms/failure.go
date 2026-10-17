package rooms

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/runtime"
)

func (m *Manager) recordReconcileError(ctx context.Context, id string, cause error) error {
	room, err := m.Get(ctx, id)
	if err != nil {
		return err
	}
	rt, err := m.runtime(ctx, id)
	if err != nil {
		return err
	}
	if room.State == "failed" && rt.Failure == nil {
		rt.Failure = room.LastError
	}
	code := "internal_error"
	var business *model.Error
	if errors.As(cause, &business) {
		code = business.Code
	}
	message := roomDiagnostic(cause.Error(), rt)
	room.LastError = model.Failure(code, message)
	rt.ReconcileError = true
	slog.Error("room reconciliation will retry", "room", id, "code", code, "error", message)
	return m.save(ctx, room, rt)
}

var roomSignedQuery = regexp.MustCompile(`([?&](?:sig|token)=)[^&\s"']+`)
var roomBearer = regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9._~-]+`)

func roomDiagnostic(message string, rt Runtime) string {
	if rt.Spec != nil {
		for _, secret := range []string{rt.Spec.RoomToken, rt.Spec.RCONPassword} {
			if secret != "" {
				message = strings.ReplaceAll(message, secret, "[redacted]")
				message = strings.ReplaceAll(message, base64.StdEncoding.EncodeToString([]byte(secret)), "[redacted]")
			}
		}
	}
	return roomBearer.ReplaceAllString(roomSignedQuery.ReplaceAllString(message, "${1}[redacted]"), "${1}[redacted]")
}

func (m *Manager) failWithError(ctx context.Context, room model.Room, rt Runtime, failure *model.Error) error {
	rt.Failure = failure
	room.LastError = failure
	if err := m.save(ctx, room, rt); err != nil {
		return err
	}
	return m.reconcileFailure(ctx, room, rt)
}

func (m *Manager) reconcileFailure(ctx context.Context, room model.Room, rt Runtime) error {
	status, err := m.observe(ctx, room, rt)
	if err != nil {
		return err
	}
	if status.ProcessExists && !rt.FailureLogsCaptured {
		logContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		logs, logErr := m.Backend.Logs(logContext, rt.Resource, runtime.LogOptions{Tail: 200, Diagnostics: true})
		cancel()
		if logErr != nil {
			slog.Warn("failed room diagnostic logs unavailable", "room", room.ID)
			logs = "服务器诊断日志暂时无法读取。\n"
		}
		lines := strings.Split(strings.TrimRight(logs, "\n"), "\n")
		if len(lines) > 200 {
			lines = lines[len(lines)-200:]
		}
		logs = roomDiagnostic(strings.Join(lines, "\n")+"\n", rt)
		if err = m.DB.Put(ctx, "room_failure_log", fmt.Sprintf("%s:%d", room.ID, room.Generation), logs); err != nil {
			return err
		}
		rt.FailureLogsCaptured = true
	}
	room.LastError, room.IdleSince = rt.Failure, nil
	room.Players = []model.Player{}
	rt.Online = 0
	if status.ProcessExists {
		room.State, room.Phase = "starting", "stopping failed server"
		if err = m.save(ctx, room, rt); err != nil {
			return err
		}
	}
	if rt.Resource.ID != "" {
		if err = m.Backend.StopRoom(ctx, rt.Resource, runtime.StopOptions{Timeout: 120 * time.Second}); err != nil {
			return err
		}
	}
	status, err = m.observe(ctx, room, rt)
	if err != nil {
		return err
	}
	if status.ProcessExists {
		return nil
	}
	room.State, room.Phase = "failed", "failed"
	rt.ProcessNotReadySince, rt.NodeMissingSince = nil, nil
	return m.save(ctx, room, rt)
}

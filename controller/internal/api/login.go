package api

import (
	"io"
	"net/http"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
)

func (s *Server) invite(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	var request struct {
		User string `json:"user"`
		TTL  string `json:"ttl"`
	}
	if err := decode(r, &request, false); err != nil {
		return err
	}
	invitation, err := s.Auth.Invite(r.Context(), id, request.User, request.TTL)
	if err != nil {
		return err
	}
	return respond(w, 201, invitation)
}

func (s *Server) redeem(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if !s.Auth.AllowRedemption(r) {
		w.Header().Set("Retry-After", "6")
		return model.Failure("rate_limited", "尝试次数过多，请稍后再试")
	}
	var request struct {
		Code   string `json:"code"`
		Client string `json:"client"`
	}
	if err := decode(r, &request, false); err != nil {
		return err
	}
	defer controller.SetReadDeadline(time.Time{})
	login, err := s.Auth.Redeem(r.Context(), request.Code, request.Client)
	if err != nil {
		return err
	}
	return respond(w, 200, login)
}

func (s *Server) invitationPage(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, err := io.WriteString(w, "<!doctype html><html lang=\"zh-CN\"><meta charset=\"utf-8\"><title>Vex 登录邀请</title><p>请在 ruri 的 设置 → 实验性功能 → Vex 服务器 → 添加服务器 中粘贴这个链接，或执行 <code>vex login &lt;链接&gt;</code>。</p></html>")
	return err
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	user := id.User
	if id.LocalAdmin {
		user = r.PathValue("user")
	}
	values, err := s.Auth.Sessions(r.Context(), user, id.SessionID)
	if err != nil {
		return err
	}
	return respond(w, 200, values)
}

func (s *Server) sessionDelete(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	user := id.User
	if id.LocalAdmin {
		user = r.PathValue("user")
	}
	if err := s.Auth.Revoke(r.Context(), user, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	if id.SessionID == "" {
		return model.Failure("conflict", "服务账号令牌由 operator 配置管理")
	}
	if err := s.Auth.Revoke(r.Context(), id.User, id.SessionID); err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	for path, operation := range map[string]handler{
		"POST /v2/auth/invites":                       (*Server).invite,
		"GET /v2/admin/users/{user}/sessions":         (*Server).sessions,
		"DELETE /v2/admin/users/{user}/sessions/{id}": (*Server).sessionDelete,
	} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if err := operation(s, w, r, auth.Identity{LocalAdmin: true}); err != nil {
				writeError(w, err)
			}
		})
	}
	return mux
}

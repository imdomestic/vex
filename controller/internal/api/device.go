package api

import (
	"net/http"
	"strconv"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
)

func (s *Server) deviceStart(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	if s.Device == nil {
		return model.Failure("provider_unavailable", "GitHub 登录尚未启用")
	}
	if !s.Auth.AllowRedemption(r) {
		return model.Failure("rate_limited", "登录请求过于频繁")
	}
	var request struct{}
	if err := decode(r, &request, true); err != nil {
		return err
	}
	result, err := s.Device.Start(r.Context())
	if err != nil {
		return err
	}
	return respond(w, 200, result)
}

func (s *Server) devicePoll(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	if s.Device == nil {
		return model.Failure("provider_unavailable", "GitHub 登录尚未启用")
	}
	var request struct {
		Handle string `json:"handle"`
		Client string `json:"client"`
	}
	if err := decode(r, &request, false); err != nil {
		return err
	}
	login, interval, err := s.Device.Poll(r.Context(), request.Handle, request.Client)
	if interval > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(interval, 10))
	}
	if err != nil {
		return err
	}
	if login != nil {
		return respond(w, 200, login)
	}
	return respond(w, 202, struct {
		Status   string `json:"status"`
		Interval int64  `json:"interval"`
	}{"pending", interval})
}

func (s *Server) deviceCancel(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	if s.Device == nil {
		return model.Failure("provider_unavailable", "GitHub 登录尚未启用")
	}
	var request struct {
		Handle string `json:"handle"`
	}
	if err := decode(r, &request, false); err != nil {
		return err
	}
	if err := s.Device.Cancel(r.Context(), request.Handle); err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

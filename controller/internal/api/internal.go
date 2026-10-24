package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"

	"github.com/imdomestic/vex/controller/internal/model"
)

func (s *Server) InternalHandler(sourceAllowed func(context.Context, string) (bool, error)) http.Handler {
	handler := s.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			writeError(w, model.Failure("forbidden", "内部来源无效"))
			return
		}
		address, err := netip.ParseAddr(host)
		if err != nil {
			writeError(w, model.Failure("forbidden", "内部来源无效"))
			return
		}
		allowed := address.IsLoopback()
		if local, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok {
			localAddress, valid := netip.AddrFromSlice(local.IP)
			allowed = allowed || valid && localAddress.Unmap() == address.Unmap()
		}
		if !allowed && sourceAllowed != nil {
			allowed, err = sourceAllowed(r.Context(), address.Unmap().String())
			if err != nil {
				slog.Error("internal HTTP source check failed", "error", err)
				writeError(w, model.Failure("internal_error", "内部来源验证暂时不可用"))
				return
			}
		}
		if !allowed {
			writeError(w, model.Failure("forbidden", "内部来源无效"))
			return
		}
		handler.ServeHTTP(w, r)
	})
}

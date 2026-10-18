package wake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	mcnet "github.com/Tnze/go-mc/net"
	pk "github.com/Tnze/go-mc/net/packet"
	"github.com/google/uuid"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"golang.org/x/time/rate"
)

const SleepingMOTD = "预览服已休眠，连接即可唤醒"

type Options struct {
	Listen        string
	Zone          string
	RateLimit     time.Duration
	SourceAllowed func(context.Context, netip.Addr) (bool, error)
}

type Lookup func(context.Context, string, string) (model.Room, error)
type Authorize func(context.Context, string, string, string, uuid.UUID) error
type Start func(context.Context, string, string, string, uuid.UUID) error

type Server struct {
	options   Options
	lookup    Lookup
	authorize Authorize
	start     Start
	mu        sync.Mutex
	last      map[string]time.Time
	peers     map[netip.Addr]*peerLimit
}

type peerLimit struct {
	limiter *rate.Limiter
	active  int
	last    time.Time
}

var roomPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var playerPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)

func New(options Options, lookup Lookup, authorize Authorize, start Start) (*Server, error) {
	if options.Zone == "" || options.Zone != strings.ToLower(options.Zone) || strings.ContainsAny(options.Zone, "/:\x00 ") || options.RateLimit < time.Minute || options.SourceAllowed == nil || lookup == nil || authorize == nil || start == nil {
		return nil, fmt.Errorf("invalid wake configuration")
	}
	return &Server{options: options, lookup: lookup, authorize: authorize, start: start, last: map[string]time.Time{}, peers: map[netip.Addr]*peerLimit{}}, nil
}

func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.options.Listen)
	if err != nil {
		return err
	}
	return s.Serve(ctx, listener)
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	defer listener.Close()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	var workers sync.WaitGroup
	defer workers.Wait()
	capacity := make(chan struct{}, 64)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		peer, err := netip.ParseAddrPort(conn.RemoteAddr().String())
		if err != nil || !s.acquire(peer.Addr().Unmap(), time.Now()) {
			conn.Close()
			continue
		}
		select {
		case capacity <- struct{}{}:
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer s.release(peer.Addr().Unmap())
				defer func() { <-capacity }()
				defer conn.Close()
				cancel := context.AfterFunc(ctx, func() { conn.Close() })
				defer cancel()
				request, done := context.WithTimeout(ctx, 20*time.Second)
				defer done()
				if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err == nil {
					allowed, err := s.options.SourceAllowed(request, peer.Addr().Unmap())
					if err != nil || !allowed {
						return
					}
					s.accept(request, mcnet.WrapConn(conn))
				}
			}()
		default:
			s.release(peer.Addr().Unmap())
			conn.Close()
		}
	}
}

func (s *Server) target(host string) (string, string, error) {
	// Forge 在主机名后附加协议标记；路由身份仅取首个主机名。
	host, _, _ = strings.Cut(host, "\x00")
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	suffix := "." + s.options.Zone
	if !strings.HasSuffix(host, suffix) {
		return "", "", fmt.Errorf("hostname is outside this server zone")
	}
	room, project, ok := strings.Cut(strings.TrimSuffix(host, suffix), "--")
	if !ok || !config.ValidID(project) || !roomPattern.MatchString(room) || strings.HasSuffix(room, "-") || strings.Contains(room, "--") || len(room)+2+len(project) > 63 {
		return "", "", fmt.Errorf("hostname does not identify a project room")
	}
	return project, room, nil
}

func (s *Server) accept(ctx context.Context, conn *mcnet.Conn) {
	var p pk.Packet
	if err := conn.ReadPacket(&p); err != nil || p.ID != 0 || len(p.Data) > 1024 {
		return
	}
	var protocol, intention pk.VarInt
	var host pk.String
	var port pk.UnsignedShort
	if err := p.Scan(&protocol, boundedField{&host, 255}, &port, &intention); err != nil {
		return
	}
	project, roomID, err := s.target(string(host))
	if err != nil {
		return
	}
	room, err := s.lookup(ctx, project, roomID)
	if err != nil || room.Project != project || room.ID != roomID || room.Kind != "branch" && room.Kind != "pr" || room.State == "closed" || room.Label == "finished" {
		return
	}
	switch intention {
	case 1:
		s.status(conn, protocol)
	case 2:
		if protocol < 764 {
			disconnect(conn, "连接唤醒需要 Minecraft 1.20.2 或更新版本")
			return
		}
		if err := conn.ReadPacket(&p); err != nil || p.ID != 0 || len(p.Data) > 128 {
			return
		}
		var name pk.String
		var claimed pk.UUID
		if err := p.Scan(boundedField{&name, 16}, &claimed); err != nil || !playerPattern.MatchString(string(name)) || uuid.UUID(claimed) == uuid.Nil {
			return
		}
		// login start 的身份由客户端声明；进入游戏时由 Minecraft online-mode 验证。
		if err = s.authorize(ctx, project, roomID, string(name), uuid.UUID(claimed)); err != nil {
			disconnect(conn, "此账号没有唤醒该项目房间的权限")
			return
		}
		if !s.allow(project+"/"+roomID, time.Now()) {
			disconnect(conn, "此房间刚刚收到唤醒请求，请稍候重新连接")
			return
		}
		if err = s.start(ctx, project, roomID, string(name), uuid.UUID(claimed)); err != nil {
			var failure *model.Error
			message := "无法唤醒此房间，请联系服务器管理员"
			if errors.As(err, &failure) {
				message = failure.Message
			}
			disconnect(conn, message)
			return
		}
		disconnect(conn, "房间正在启动，约 1 分钟后重新连接")
	}
}

func (s *Server) acquire(peer netip.Addr, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for address, limit := range s.peers {
		if limit.active == 0 && now.Sub(limit.last) > 5*time.Minute {
			delete(s.peers, address)
		}
	}
	limit := s.peers[peer]
	if limit == nil {
		if len(s.peers) >= 4096 {
			return false
		}
		limit = &peerLimit{limiter: rate.NewLimiter(4, 8)}
		s.peers[peer] = limit
	}
	limit.last = now
	if limit.active >= 8 || !limit.limiter.AllowN(now, 1) {
		return false
	}
	limit.active++
	return true
}

func (s *Server) release(peer netip.Addr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[peer].active--
}

func (s *Server) allow(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for room, last := range s.last {
		if now.Sub(last) >= s.options.RateLimit {
			delete(s.last, room)
		}
	}
	if _, exists := s.last[key]; exists {
		return false
	}
	s.last[key] = now
	return true
}

func (s *Server) status(conn *mcnet.Conn, protocol pk.VarInt) {
	var p pk.Packet
	if err := conn.ReadPacket(&p); err != nil || p.ID != 0 || len(p.Data) != 0 {
		return
	}
	response := struct {
		Version struct {
			Name     string `json:"name"`
			Protocol int32  `json:"protocol"`
		} `json:"version"`
		Players struct {
			Max    int `json:"max"`
			Online int `json:"online"`
		} `json:"players"`
		Description struct {
			Text string `json:"text"`
		} `json:"description"`
	}{}
	response.Version.Name, response.Version.Protocol = "Vex", int32(protocol)
	response.Players.Max = 1
	response.Description.Text = SleepingMOTD
	data, err := json.Marshal(response)
	if err != nil || conn.WritePacket(pk.Marshal(0, pk.String(data))) != nil {
		return
	}
	if err = conn.ReadPacket(&p); err != nil || p.ID != 1 || len(p.Data) != 8 {
		return
	}
	conn.WritePacket(p)
}

func disconnect(conn *mcnet.Conn, reason string) {
	data, err := json.Marshal(struct {
		Text string `json:"text"`
	}{reason})
	if err == nil {
		conn.WritePacket(pk.Marshal(0, pk.String(data)))
	}
}

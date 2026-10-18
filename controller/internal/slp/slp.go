package slp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Tnze/go-mc/chat"
	mcnet "github.com/Tnze/go-mc/net"
	pk "github.com/Tnze/go-mc/net/packet"
	"github.com/imdomestic/vex/controller/internal/model"
)

type Status struct {
	Description chat.Message `json:"description"`
	Players     struct {
		Online int `json:"online"`
		Max    int `json:"max"`
		Sample []struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"sample"`
	} `json:"players"`
}

func Probe(ctx context.Context, address, host string, port int) (Status, error) {
	return ProbeFrom(ctx, address, host, port, nil)
}

func ProbeFrom(ctx context.Context, address, host string, port int, source net.IP) (Status, error) {
	var result Status
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	dialer := &net.Dialer{}
	if source != nil {
		dialer.LocalAddr = &net.TCPAddr{IP: source}
	}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return result, err
	}
	mc := mcnet.WrapConn(conn)
	if err = mc.WritePacket(pk.Marshal(0, pk.VarInt(-1), pk.String(host), pk.UnsignedShort(port), pk.VarInt(1))); err != nil {
		return result, err
	}
	if err = mc.WritePacket(pk.Marshal(0)); err != nil {
		return result, err
	}
	var packet pk.Packet
	if err = mc.ReadPacket(&packet); err != nil {
		return result, err
	}
	if packet.ID != 0 {
		return result, fmt.Errorf("unexpected SLP packet %d", packet.ID)
	}
	var payload pk.String
	if err = packet.Scan(&payload); err != nil {
		return result, err
	}
	if err = json.Unmarshal([]byte(payload), &result); err != nil {
		return result, err
	}
	if result.Players.Online < 0 {
		return result, fmt.Errorf("negative SLP player count")
	}
	return result, nil
}

func Matches(status Status, room model.Room) bool {
	fields := strings.Fields(status.Description.ClearString())
	expected := map[string]string{"project": room.Project, "room": room.ID, "build": room.BuildID, "generation": fmt.Sprint(room.Generation)}
	seen := map[string]bool{}
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if want, present := expected[key]; ok && present {
			if seen[key] || value != want {
				return false
			}
			seen[key] = true
		}
	}
	return len(seen) == len(expected)
}

func (s Status) Sample() []model.Player {
	players := []model.Player{}
	for _, p := range s.Players.Sample {
		players = append(players, model.Player{Name: p.Name, UUID: p.ID})
	}
	return players
}

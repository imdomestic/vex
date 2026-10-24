package wake

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	mcnet "github.com/Tnze/go-mc/net"
	pk "github.com/Tnze/go-mc/net/packet"
)

func TestHostAndRoomRateBoundaries(t *testing.T) {
	server := &Server{options: Options{Zone: "example.test", RateLimit: time.Minute}, last: map[string]time.Time{}}
	project, room, err := server.target("Main-Fabric--project.example.test.\x00FML3\x00")
	if err != nil || project != "project" || room != "main-fabric" {
		t.Fatal("original hostname did not identify the room", err)
	}
	for _, host := range []string{"main-fabric--project.example.test.attacker.test", "main-fabric--other--project.example.test", "main-fabric.example.test", "main-fabric--project-.example.test", strings.Repeat("a", 63) + "--project.example.test"} {
		if _, _, err = server.target(host); err == nil {
			t.Fatalf("accepted invalid hostname %q", host)
		}
	}
	now := time.Now()
	if !server.allow("project/main-fabric", now) || server.allow("project/main-fabric", now.Add(59*time.Second)) || !server.allow("other/main-fabric", now) || !server.allow("project/main-fabric", now.Add(time.Minute)) {
		t.Fatal("room rate limit crossed a project or time boundary")
	}
}

func TestSleepingStatusUsesRealMinecraftCodec(t *testing.T) {
	serverSocket, clientSocket := net.Pipe()
	defer serverSocket.Close()
	defer clientSocket.Close()
	if err := clientSocket.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		(&Server{}).status(mcnet.WrapConn(serverSocket), 774)
	}()
	client := mcnet.WrapConn(clientSocket)
	if err := client.WritePacket(pk.Marshal(0)); err != nil {
		t.Fatal(err)
	}
	var response pk.Packet
	if err := client.ReadPacket(&response); err != nil || response.ID != 0 {
		t.Fatal("status response failed", err)
	}
	var data pk.String
	if err := response.Scan(&data); err != nil {
		t.Fatal(err)
	}
	var status struct {
		Description struct {
			Text string `json:"text"`
		} `json:"description"`
	}
	if err := json.Unmarshal([]byte(data), &status); err != nil || status.Description.Text != SleepingMOTD || strings.Contains(string(data), "generation=") || strings.Contains(string(data), "build=") {
		t.Fatal("sleeping status contains an invalid ready identity", err)
	}
	if err := client.WritePacket(pk.Marshal(1, pk.Long(42))); err != nil {
		t.Fatal(err)
	}
	if err := client.ReadPacket(&response); err != nil || response.ID != 1 {
		t.Fatal("status pong failed", err)
	}
	var pong pk.Long
	if err := response.Scan(&pong); err != nil || pong != 42 {
		t.Fatal("status pong differs", err)
	}
	<-finished
}

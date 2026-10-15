package rooms

import (
	"testing"

	"github.com/imdomestic/chorus-testbed/controller/internal/kube"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
)

func TestLostRecoveryPreservesIdentityAndStopIntent(t *testing.T) {
	room := model.Room{State: "lost", Label: "testing", Generation: 4, BuildID: "fabric-fc16eaac252b"}
	status := kube.RoomStatus{PodExists: true, Ready: true, Generation: 4, BuildID: room.BuildID}
	if lostRecoveryState(room, Runtime{}, status) != "starting" {
		t.Fatal("matching ready Pod must be reverified")
	}
	for _, previous := range []string{"stopping", "archiving"} {
		if lostRecoveryState(room, Runtime{LostFrom: previous}, status) != "stopping" {
			t.Fatal("lost recovery discarded stop intent")
		}
	}
	room.Label = "finished"
	if lostRecoveryState(room, Runtime{}, status) != "stopping" {
		t.Fatal("finished room was resumed")
	}
	room.Label = "testing"
	status.Generation++
	if lostRecoveryState(room, Runtime{}, status) != "stopping" {
		t.Fatal("old generation was resumed")
	}
	status.Generation = room.Generation
	status.BuildID = "fabric-cb7f59da8fe5"
	if lostRecoveryState(room, Runtime{}, status) != "stopping" {
		t.Fatal("different build was resumed")
	}
	status.BuildID = room.BuildID
	status.Ready = false
	if lostRecoveryState(room, Runtime{}, status) != "stopping" {
		t.Fatal("unready Pod was resumed")
	}
	if lostRecoveryState(room, Runtime{}, kube.RoomStatus{}) != "stopping" {
		t.Fatal("missing Pod was resumed")
	}
}

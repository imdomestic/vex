package slp

import (
	"testing"

	"github.com/Tnze/go-mc/chat"
	"github.com/imdomestic/vex/controller/internal/model"
)

func TestIdentityRequiresFourExactFields(t *testing.T) {
	room := model.Room{Project: "example", ID: "main-fabric", BuildID: "fabric-123456789012", Generation: 1}
	for _, test := range []struct {
		text string
		want bool
	}{
		{"§7vex\n§8project=example room=main-fabric build=fabric-123456789012 generation=1", true},
		{"project=example room=main-fabric build=fabric-123456789012 generation=10", false},
		{"project=example room=main-fabric-other build=fabric-123456789012 generation=1", false},
		{"project=example room=main-fabric build=fabric-123456789013 generation=1", false},
		{"project=example room=main-fabric build=fabric-123456789012", false},
		{"project=example room=main-fabric build=fabric-123456789012 generation=1 generation=2", false},
		{"project=other room=main-fabric build=fabric-123456789012 generation=1", false},
		{"room=main-fabric build=fabric-123456789012 generation=1", false},
	} {
		if got := Matches(Status{Description: chat.Text(test.text)}, room); got != test.want {
			t.Errorf("Matches(%q)=%t", test.text, got)
		}
	}
}

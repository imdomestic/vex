package slp

import (
	"testing"

	"github.com/Tnze/go-mc/chat"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
)

func TestIdentityRequiresThreeExactFields(t *testing.T) {
	room := model.Room{ID: "main-fabric", BuildID: "fabric-123456789012", Generation: 1}
	for _, test := range []struct {
		text string
		want bool
	}{
		{"§7chorus\n§8room=main-fabric build=fabric-123456789012 generation=1", true},
		{"room=main-fabric build=fabric-123456789012 generation=10", false},
		{"room=main-fabric-other build=fabric-123456789012 generation=1", false},
		{"room=main-fabric build=fabric-123456789013 generation=1", false},
		{"room=main-fabric build=fabric-123456789012", false},
		{"room=main-fabric build=fabric-123456789012 generation=1 generation=2", false},
	} {
		if got := Matches(Status{Description: chat.Text(test.text)}, room); got != test.want {
			t.Errorf("Matches(%q)=%t", test.text, got)
		}
	}
}

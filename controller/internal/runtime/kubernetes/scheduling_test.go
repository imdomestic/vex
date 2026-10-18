package kubernetes

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestRoomSchedulingRespectsHardTaints(t *testing.T) {
	for _, effect := range []corev1.TaintEffect{corev1.TaintEffectNoSchedule, corev1.TaintEffectNoExecute, corev1.TaintEffectPreferNoSchedule} {
		t.Run(string(effect), func(t *testing.T) {
			node := corev1.Node{Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: "vex.imdomestic.com/maintenance", Value: "true", Effect: effect}}}}
			if got := roomNodeSchedulable(node, roomTolerations()); got != (effect == corev1.TaintEffectPreferNoSchedule) {
				t.Fatal("room scheduling ignored taint effect", effect)
			}
			exact := []corev1.Toleration{{Key: "vex.imdomestic.com/maintenance", Operator: corev1.TolerationOpEqual, Value: "true", Effect: effect}}
			if !roomNodeSchedulable(node, exact) {
				t.Fatal("matching Kubernetes toleration was rejected")
			}
			node.Spec.Unschedulable = true
			if roomNodeSchedulable(node, exact) {
				t.Fatal("toleration bypassed node cordon")
			}
		})
	}
	node := corev1.Node{Spec: corev1.NodeSpec{Taints: []corev1.Taint{
		{Key: "maintenance", Value: "one", Effect: corev1.TaintEffectNoSchedule},
		{Key: "storage", Value: "two", Effect: corev1.TaintEffectNoExecute},
	}}}
	partial := []corev1.Toleration{{Key: "maintenance", Operator: corev1.TolerationOpExists}}
	if roomNodeSchedulable(node, partial) {
		t.Fatal("one tolerated taint concealed another blocking taint")
	}
	if !roomNodeSchedulable(corev1.Node{}, roomTolerations()) {
		t.Fatal("untainted node became unschedulable")
	}
}

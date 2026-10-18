package kubernetes

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

func roomTolerations() []corev1.Toleration {
	return nil
}

func roomNodeSchedulable(node corev1.Node, tolerations []corev1.Toleration) bool {
	if node.Spec.Unschedulable {
		return false
	}
	for _, taint := range node.Spec.Taints {
		if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		tolerated := false
		for _, toleration := range tolerations {
			if toleration.ToleratesTaint(klog.Background(), &taint, false) {
				tolerated = true
				break
			}
		}
		if !tolerated {
			return false
		}
	}
	return true
}

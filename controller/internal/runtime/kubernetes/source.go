package kubernetes

import (
	"context"
	"net/netip"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *Client) RoomSource(ctx context.Context, source string) (bool, error) {
	address, err := netip.ParseAddr(source)
	if err != nil {
		return false, err
	}
	pods, err := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{
		LabelSelector: "vex.imdomestic.com/managed=true,vex.imdomestic.com/project=" + c.cfg.ID,
		FieldSelector: "status.podIP=" + address.Unmap().String(),
	})
	if err != nil {
		return false, err
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || (pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending) || pod.Spec.ServiceAccountName != "default" || pod.Spec.HostNetwork {
			continue
		}
		name, err := objectName(pod.Labels["vex.imdomestic.com/room"])
		if err != nil {
			continue
		}
		owner := metav1.GetControllerOf(&pod)
		if owner == nil || owner.Kind != "StatefulSet" || owner.APIVersion != "apps/v1" || owner.Name != name {
			continue
		}
		stateful, err := c.api.AppsV1().StatefulSets(c.ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if stateful.UID == owner.UID && stateful.Labels["vex.imdomestic.com/project"] == c.cfg.ID && stateful.Labels["vex.imdomestic.com/managed"] == "true" {
			return true, nil
		}
	}
	return false, nil
}

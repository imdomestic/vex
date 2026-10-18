package kubernetes

import (
	"context"
	"net/netip"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

func (c *Client) RoomSource(ctx context.Context, source string) (bool, error) {
	address, err := netip.ParseAddr(source)
	if err != nil {
		return false, err
	}
	pods, err := c.sourcePods(ctx, address.Unmap().String())
	if err != nil {
		return false, err
	}
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil || (pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending) || pod.Spec.ServiceAccountName != "default" || pod.Spec.HostNetwork {
			continue
		}
		name, err := objectName(pod.Labels["vex.imdomestic.com/room"])
		if err != nil {
			continue
		}
		owner := metav1.GetControllerOf(&pod)
		if owner == nil {
			continue
		}
		switch {
		case owner.Kind == "StatefulSet" && owner.APIVersion == "apps/v1" && owner.Name == name:
			stateful, err := c.api.AppsV1().StatefulSets(c.ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if stateful.DeletionTimestamp == nil && stateful.UID == owner.UID && c.sourceLabels(stateful.Labels, pod.Labels) {
				return true, nil
			}
		case owner.Kind == "Job" && owner.APIVersion == "batch/v1" && pod.Labels["app.kubernetes.io/component"] == "archive":
			generation, err := strconv.ParseInt(pod.Labels["vex.imdomestic.com/generation"], 10, 64)
			if err != nil || generation <= 0 || owner.Name != archiveJobName(pod.Labels["vex.imdomestic.com/room"], generation) {
				continue
			}
			job, err := c.api.BatchV1().Jobs(c.ns).Get(ctx, owner.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if job.DeletionTimestamp == nil && job.UID == owner.UID && job.Labels["app.kubernetes.io/component"] == "archive" && c.sourceLabels(job.Labels, pod.Labels) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (c *Client) sourcePods(ctx context.Context, address string) ([]corev1.Pod, error) {
	options := metav1.ListOptions{
		LabelSelector: "vex.imdomestic.com/managed=true,vex.imdomestic.com/project=" + c.cfg.ID,
		FieldSelector: "status.podIP=" + address,
	}
	pods, err := c.api.CoreV1().Pods(c.ns).List(ctx, options)
	if err != nil || len(pods.Items) > 0 {
		if err != nil {
			return nil, err
		}
		return pods.Items, nil
	}
	// 容器可以在 kubelet 发布 Pod IP 之前请求下载或上传。
	// 等待真实状态事件，来源身份确认完成后才允许请求继续。
	pending, err := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{LabelSelector: options.LabelSelector, FieldSelector: "status.phase=Pending"})
	if err != nil {
		return nil, err
	}
	if len(pending.Items) == 0 {
		current, err := c.api.CoreV1().Pods(c.ns).List(ctx, options)
		if err != nil {
			return nil, err
		}
		return current.Items, nil
	}
	observing, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	options.ResourceVersion = pods.ResourceVersion
	events, err := c.api.CoreV1().Pods(c.ns).Watch(observing, options)
	if err != nil {
		return nil, err
	}
	defer events.Stop()
	for {
		select {
		case <-observing.Done():
			return nil, ctx.Err()
		case event, open := <-events.ResultChan():
			if !open {
				return nil, ctx.Err()
			}
			if event.Type == watch.Error {
				return nil, apierrors.FromObject(event.Object)
			}
			if pod, ok := event.Object.(*corev1.Pod); ok && (event.Type == watch.Added || event.Type == watch.Modified) && pod.Status.PodIP == address {
				return []corev1.Pod{*pod}, nil
			}
		}
	}
}

func (c *Client) sourceLabels(owner, pod map[string]string) bool {
	if !c.managed(owner, pod["vex.imdomestic.com/room"]) {
		return false
	}
	for _, key := range []string{"vex.imdomestic.com/build", "vex.imdomestic.com/generation"} {
		if pod[key] == "" || owner[key] != pod[key] {
			return false
		}
	}
	return true
}

package kubernetes

import (
	"context"
	"fmt"
	"time"

	"github.com/imdomestic/vex/controller/internal/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeleteWorld 按 UID 删除已停止房间的世界卷并等待删除完成，返回被删除 PVC 的 UID。
// StatefulSet 保留；下次启动时按卷模板创建新的空卷。
func (c *Client) DeleteWorld(ctx context.Context, ref runtime.ResourceRef) (string, error) {
	room, err := c.referenceRoom(ref)
	if err != nil {
		return "", err
	}
	name, err := objectName(room.ID)
	if err != nil {
		return "", err
	}
	status, err := c.verifyCurrent(ctx, ref)
	if err != nil {
		return "", err
	}
	if status.ProcessExists {
		return "", fmt.Errorf("room Pod still exists")
	}
	pods, err := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{LabelSelector: "vex.imdomestic.com/room=" + room.ID})
	if err != nil {
		return "", err
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
			return "", fmt.Errorf("room world volume is still used by Pod %s", pod.Name)
		}
	}
	pvc, err := c.api.CoreV1().PersistentVolumeClaims(c.ns).Get(ctx, "data-"+name+"-0", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if !c.managed(pvc.Labels, room.ID) {
		return "", fmt.Errorf("PVC is not owned by this room")
	}
	uid := pvc.UID
	if err = c.api.CoreV1().PersistentVolumeClaims(c.ns).Delete(ctx, pvc.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
		return "", err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		current, err := c.api.CoreV1().PersistentVolumeClaims(c.ns).Get(ctx, pvc.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) || err == nil && current.UID != uid {
			return string(uid), nil
		}
		if err != nil {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

package kube

import (
	"context"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *Client) FailureLogs(ctx context.Context, id string, tail int64) (string, error) {
	if tail < 1 || tail > 200 {
		return "", fmt.Errorf("log tail must be between 1 and 200")
	}
	name, err := objectName(id)
	if err != nil {
		return "", err
	}
	pod, err := c.api.CoreV1().Pods(c.ns).Get(ctx, name+"-0", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !managed(pod.Labels, id) {
		return "", fmt.Errorf("Pod is not owned by this room")
	}
	var output strings.Builder
	read := func(container string, previous bool) error {
		stream, e := c.api.CoreV1().Pods(c.ns).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container, Previous: previous, TailLines: &tail, Timestamps: true}).Stream(ctx)
		if e != nil {
			return e
		}
		defer stream.Close()
		fmt.Fprintf(&output, "[%s previous=%t]\n", container, previous)
		_, e = io.Copy(&output, io.LimitReader(stream, 2<<20))
		return e
	}
	appendStatus := func(status corev1.ContainerStatus) error {
		if status.State.Waiting != nil {
			fmt.Fprintf(&output, "[%s waiting=%s]\n", status.Name, status.State.Waiting.Reason)
		}
		if status.RestartCount > 0 && status.LastTerminationState.Terminated != nil {
			if err := read(status.Name, true); err != nil {
				return err
			}
		}
		if status.State.Running != nil || status.State.Terminated != nil {
			return read(status.Name, false)
		}
		return nil
	}
	for _, status := range pod.Status.InitContainerStatuses {
		if status.State.Terminated != nil && status.State.Terminated.ExitCode == 0 {
			continue
		}
		if err = appendStatus(status); err != nil {
			return "", err
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "mc" {
			if err = appendStatus(status); err != nil {
				return "", err
			}
		}
	}
	if output.Len() == 0 {
		fmt.Fprintf(&output, "[Pod phase=%s; no container logs available]\n", pod.Status.Phase)
	}
	return output.String(), nil
}

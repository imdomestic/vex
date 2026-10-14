package kube

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ArchiveSpec struct {
	Room model.Room
	RoomToken string
}

type ArchiveStatus struct {
	Exists bool
	Complete bool
	Failed bool
	WorldMissing bool
	Message string
}

func (c *Client) Archive(ctx context.Context, spec ArchiveSpec) (ArchiveStatus, error) {
	var status ArchiveStatus
	name, err := objectName(spec.Room.ID)
	if err != nil { return status, err }
	if spec.Room.Generation < 1 || spec.RoomToken == "" { return status, fmt.Errorf("archive requires room generation and credentials") }
	observed, err := c.ObserveRoom(ctx, spec.Room.ID)
	if err != nil { return status, err }
	if observed.PodExists { return status, fmt.Errorf("room Pod must exit before archive") }
	if observed.Exists && (observed.Generation != spec.Room.Generation || observed.BuildID != spec.Room.BuildID) { return status, fmt.Errorf("archive room identity mismatch") }
	jobName := fmt.Sprintf("archive-%s-%d", spec.Room.ID, spec.Room.Generation)
	job, err := c.api.BatchV1().Jobs(c.ns).Get(ctx, jobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		claim, e := c.api.CoreV1().PersistentVolumeClaims(c.ns).Get(ctx, "data-"+name+"-0", metav1.GetOptions{})
		if e != nil { return status, e }
		if !managed(claim.Labels, spec.Room.ID) { return status, fmt.Errorf("PVC is not owned by this room") }
		labels := roomLabels(RoomSpec{Room: spec.Room})
		labels["app.kubernetes.io/component"] = "archive"
		backoff, ttl, deadline, automount := int32(2), int32(600), int64(900), false
		query := url.Values{"room": {spec.Room.ID}, "build": {spec.Room.BuildID}, "generation": {strconv.FormatInt(spec.Room.Generation, 10)}}
		job = &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: c.ns, Labels: labels}, Spec: batchv1.JobSpec{
			BackoffLimit: &backoff, TTLSecondsAfterFinished: &ttl, ActiveDeadlineSeconds: &deadline,
			PodFailurePolicy: &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{{Action: batchv1.PodFailurePolicyActionFailJob, OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{Operator: batchv1.PodFailurePolicyOnExitCodesOpIn, Values: []int32{42}}}}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &automount,
				Containers: []corev1.Container{{Name: "archive", Image: c.cfg.Minecraft.Image,
					Command: []string{"/bin/bash", "-euo", "pipefail", "-c", archiveScript},
					Env: []corev1.EnvVar{{Name: "TMPDIR", Value: "/data/.work"}, {Name: "GENERATION", Value: strconv.FormatInt(spec.Room.Generation, 10)}, {Name: "UPLOAD_URL", Value: c.cfg.Server.InternalBaseURL+"/v1/snapshots?"+query.Encode()}, secretEnv("CHORUS_ROOM_TOKEN", name+"-credentials", "room-token")},
					VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("512Mi")}},
				}},
				Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim.Name}}}},
			}},
		}}
		job, err = c.api.BatchV1().Jobs(c.ns).Create(ctx, job, metav1.CreateOptions{})
	}
	if err != nil { return status, err }
	if !managed(job.Labels, spec.Room.ID) || job.Labels["chorus.dev/generation"] != strconv.FormatInt(spec.Room.Generation, 10) { return status, fmt.Errorf("archive Job identity mismatch") }
	status.Exists = true
	for _, condition := range job.Status.Conditions {
		if condition.Status == corev1.ConditionTrue {
			if condition.Type == batchv1.JobComplete { status.Complete = true }
			if condition.Type == batchv1.JobFailed { status.Failed = true; status.Message = condition.Message }
		}
	}
	if status.Failed {
		pods, e := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/job-name="+jobName})
		if e != nil { return status, e }
		for _, pod := range pods.Items {
			if !managed(pod.Labels, spec.Room.ID) { return status, fmt.Errorf("archive Pod identity mismatch") }
			for _, container := range pod.Status.ContainerStatuses {
				if container.Name == "archive" && container.State.Terminated != nil && container.State.Terminated.ExitCode == 42 { status.WorldMissing = true; status.Message = "world/level.dat does not exist" }
			}
		}
	}
	return status, nil
}

func (c *Client) DeleteArchivedRoom(ctx context.Context, room model.Room, snapshot model.Snapshot) error {
	if (room.Kind != "commit" && room.Kind != "asset") || (room.State != "archiving" && room.State != "closed") || snapshot.RoomID != room.ID || snapshot.RoomGeneration != room.Generation || snapshot.BuildID != room.BuildID || snapshot.CreatedAt.IsZero() || snapshot.Size <= 0 || !sha256Pattern.MatchString(snapshot.SHA256) || (!snapshot.LocalAvailable && snapshot.TankVerifiedAt == nil) {
		return fmt.Errorf("room deletion requires a complete snapshot of this commit or asset generation")
	}
	name, err := objectName(room.ID)
	if err != nil { return err }
	observed, err := c.ObserveRoom(ctx, room.ID)
	if err != nil { return err }
	if observed.PodExists { return fmt.Errorf("room Pod still exists") }
	if observed.Exists && (observed.Generation != room.Generation || observed.BuildID != room.BuildID) { return fmt.Errorf("room generation changed before deletion") }
	job, err := c.api.BatchV1().Jobs(c.ns).Get(ctx, fmt.Sprintf("archive-%s-%d", room.ID, room.Generation), metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) { return err }
	if err == nil {
		if !managed(job.Labels, room.ID) || job.Labels["chorus.dev/generation"] != strconv.FormatInt(room.Generation, 10) { return fmt.Errorf("archive Job identity mismatch") }
		complete := false
		for _, condition := range job.Status.Conditions { if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue { complete = true } }
		if !complete { return fmt.Errorf("archive Job has not completed") }
		ttl := int32(1)
		if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != ttl {
			job.Spec.TTLSecondsAfterFinished = &ttl
			if _, err = c.api.BatchV1().Jobs(c.ns).Update(ctx, job, metav1.UpdateOptions{}); err != nil { return err }
		}
	}
	pods, err := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{LabelSelector: "chorus.dev/room="+room.ID})
	if err != nil { return err }
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed { return fmt.Errorf("room PVC is still used by a Pod") }
	}
	policy := metav1.DeletePropagationForeground
	if err = c.api.AppsV1().StatefulSets(c.ns).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy}); err != nil && !apierrors.IsNotFound(err) { return err }
	pvc, err := c.api.CoreV1().PersistentVolumeClaims(c.ns).Get(ctx, "data-"+name+"-0", metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) { return err }
	if err == nil {
		if !managed(pvc.Labels, room.ID) { return fmt.Errorf("PVC is not owned by this room") }
		if err = c.api.CoreV1().PersistentVolumeClaims(c.ns).Delete(ctx, pvc.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pvc.UID}}); err != nil && !apierrors.IsNotFound(err) { return err }
	}
	for _, kind := range []string{"service", "secret"} {
		if kind == "service" {
			object, e := c.api.CoreV1().Services(c.ns).Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(e) { continue }
			if e != nil { return e }
			if !managed(object.Labels, room.ID) { return fmt.Errorf("Service is not owned by this room") }
			err = c.api.CoreV1().Services(c.ns).Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &object.UID}})
		} else {
			object, e := c.api.CoreV1().Secrets(c.ns).Get(ctx, name+"-credentials", metav1.GetOptions{})
			if apierrors.IsNotFound(e) { continue }
			if e != nil { return e }
			if !managed(object.Labels, room.ID) { return fmt.Errorf("Secret is not owned by this room") }
			err = c.api.CoreV1().Secrets(c.ns).Delete(ctx, object.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &object.UID}})
		}
		if err != nil && !apierrors.IsNotFound(err) { return err }
	}
	return nil
}

const archiveScript = `mkdir -p /data/.work
if ! test -f /data/world/level.dat; then
  printf '%s\n' 'world/level.dat does not exist' >/dev/termination-log
  exit 42
fi
archive="/data/.work/snapshot-${GENERATION}.tar.gz"
if ! test -f "$archive"; then
  tar --create --gzip --file="${archive}.partial" --directory=/data world
  mv "${archive}.partial" "$archive"
fi
if curl --fail-with-body --silent --show-error --request POST --header "Authorization: Bearer $CHORUS_ROOM_TOKEN" --header 'Content-Type: application/gzip' --data-binary "@$archive" "$UPLOAD_URL" --output /data/.work/snapshot-response.json; then
  exit 0
else
  status=$?
  if test -s /data/.work/snapshot-response.json; then
    jq --raw-output '.error.message // "snapshot upload failed"' /data/.work/snapshot-response.json >&2
  fi
  exit "$status"
fi
`

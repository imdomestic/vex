package kubernetes

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/imdomestic/vex/controller/internal/runtime"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ArchiveStatus struct {
	Exists       bool
	Complete     bool
	Failed       bool
	WorldMissing bool
	Message      string
}

func archiveJobName(id string, generation int64) string {
	digest := sha256.Sum256([]byte(id))
	return fmt.Sprintf("archive-%x-%d", digest[:8], generation)
}

func (c *Client) ArchiveWorld(ctx context.Context, spec runtime.ArchiveSpec) (runtime.ArchiveResult, error) {
	room, err := c.referenceRoom(spec.Resource)
	if err != nil {
		return runtime.ArchiveResult{}, err
	}
	if room.Project != spec.Room.Project || room.ID != spec.Room.ID || room.Generation != spec.Room.Generation || room.BuildID != spec.Room.BuildID {
		return runtime.ArchiveResult{}, fmt.Errorf("archive resource and room identities differ")
	}
	if spec.Build.ID != room.BuildID || spec.Build.Project != room.Project || spec.Build.Java == nil {
		return runtime.ArchiveResult{}, fmt.Errorf("archive build does not match this room")
	}
	status, err := c.archive(ctx, spec)
	if err != nil {
		return runtime.ArchiveResult{}, err
	}
	result := runtime.ArchiveResult{State: runtime.ArchivePending, Message: status.Message}
	if status.WorldMissing {
		result.State = runtime.ArchiveWorldMissing
	} else if status.Failed {
		result.State = runtime.ArchiveFailed
	} else if status.Complete {
		path := filepath.Join(c.cfg.DataDir, "snapshots", room.ID, fmt.Sprintf("%s-g%d.tar.gz", room.ID, room.Generation))
		file, err := os.Open(path)
		if err != nil {
			return runtime.ArchiveResult{}, fmt.Errorf("completed archive output: %w", err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return runtime.ArchiveResult{}, err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return runtime.ArchiveResult{}, fmt.Errorf("archive output must be a nonempty regular file")
		}
		result.State, result.Path = runtime.ArchiveComplete, path
	}
	return result, nil
}

func (c *Client) archive(ctx context.Context, spec runtime.ArchiveSpec) (ArchiveStatus, error) {
	var status ArchiveStatus
	name, err := objectName(spec.Room.ID)
	if err != nil {
		return status, err
	}
	if spec.Room.Generation < 1 || spec.RoomToken == "" {
		return status, fmt.Errorf("archive requires room generation and credentials")
	}
	image := c.cfg.Runtimes[fmt.Sprintf("java%d", *spec.Build.Java)].ServerImage
	if image == "" {
		return status, fmt.Errorf("archive requires the build Java runtime image")
	}
	observed, err := c.verifyCurrent(ctx, spec.Resource)
	if err != nil {
		return status, err
	}
	if observed.ProcessExists {
		return status, fmt.Errorf("room Pod must exit before archive")
	}
	if observed.Exists && (observed.Generation != spec.Room.Generation || observed.BuildID != spec.Room.BuildID) {
		return status, fmt.Errorf("archive room identity mismatch")
	}
	jobName := archiveJobName(spec.Room.ID, spec.Room.Generation)
	job, err := c.api.BatchV1().Jobs(c.ns).Get(ctx, jobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		claim, e := c.api.CoreV1().PersistentVolumeClaims(c.ns).Get(ctx, "data-"+name+"-0", metav1.GetOptions{})
		if e != nil {
			return status, e
		}
		if !c.managed(claim.Labels, spec.Room.ID) {
			return status, fmt.Errorf("PVC is not owned by this room")
		}
		labels := roomLabels(RoomSpec{Room: spec.Room})
		labels["app.kubernetes.io/component"] = "archive"
		backoff, ttl, deadline, automount := int32(2), int32(600), int64(900), false
		query := url.Values{"room": {spec.Room.ID}, "build": {spec.Room.BuildID}, "generation": {strconv.FormatInt(spec.Room.Generation, 10)}}
		job = &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: c.ns, Labels: labels}, Spec: batchv1.JobSpec{
			BackoffLimit: &backoff, TTLSecondsAfterFinished: &ttl, ActiveDeadlineSeconds: &deadline,
			PodFailurePolicy: &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{{Action: batchv1.PodFailurePolicyActionFailJob, OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{Operator: batchv1.PodFailurePolicyOnExitCodesOpIn, Values: []int32{42}}}}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &automount,
				Containers: []corev1.Container{{Name: "archive", Image: image,
					Command:      []string{"/bin/bash", "-euo", "pipefail", "-c", archiveScript},
					Env:          []corev1.EnvVar{{Name: "TMPDIR", Value: "/data/.work"}, {Name: "GENERATION", Value: strconv.FormatInt(spec.Room.Generation, 10)}, {Name: "UPLOAD_URL", Value: c.cfg.Server.InternalBaseURL + "/v2/projects/" + c.cfg.ID + "/snapshots?" + query.Encode()}, secretEnv("VEX_ROOM_TOKEN", name+"-credentials", "room-token")},
					VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
					Resources:    corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("512Mi")}},
				}},
				Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim.Name}}}},
			}},
		}}
		job, err = c.api.BatchV1().Jobs(c.ns).Create(ctx, job, metav1.CreateOptions{})
	}
	if err != nil {
		return status, err
	}
	if !c.managed(job.Labels, spec.Room.ID) || job.Labels["vex.imdomestic.com/generation"] != strconv.FormatInt(spec.Room.Generation, 10) || job.Labels["vex.imdomestic.com/build"] != spec.Room.BuildID {
		return status, fmt.Errorf("archive Job identity mismatch")
	}
	status.Exists = true
	for _, condition := range job.Status.Conditions {
		if condition.Status == corev1.ConditionTrue {
			if condition.Type == batchv1.JobComplete {
				status.Complete = true
			}
			if condition.Type == batchv1.JobFailed {
				status.Failed = true
				status.Message = condition.Message
			}
		}
	}
	if status.Failed {
		pods, e := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/job-name=" + jobName})
		if e != nil {
			return status, e
		}
		for _, pod := range pods.Items {
			if !c.managed(pod.Labels, spec.Room.ID) {
				return status, fmt.Errorf("archive Pod identity mismatch")
			}
			for _, container := range pod.Status.ContainerStatuses {
				if container.Name == "archive" && container.State.Terminated != nil && container.State.Terminated.ExitCode == 42 {
					status.WorldMissing = true
					status.Message = "world/level.dat does not exist"
				}
			}
		}
	}
	return status, nil
}

func (c *Client) DeleteRoom(ctx context.Context, ref runtime.ResourceRef, options runtime.DeleteOptions) error {
	room, err := c.referenceRoom(ref)
	if err != nil {
		return err
	}
	if room.Kind == "branch" && !options.KeepWorld {
		return fmt.Errorf("branch room world must be retained")
	}
	if options.Snapshot == nil {
		return fmt.Errorf("room deletion requires a completed snapshot")
	}
	snapshot := *options.Snapshot
	if snapshot.Project != c.cfg.ID || snapshot.RoomID != room.ID || snapshot.RoomGeneration != room.Generation || snapshot.BuildID != room.BuildID || snapshot.CreatedAt.IsZero() || snapshot.Size <= 0 || !sha256Pattern.MatchString(snapshot.SHA256) || (!snapshot.LocalAvailable && snapshot.TankVerifiedAt == nil) {
		return fmt.Errorf("room deletion requires a complete snapshot of this generation")
	}
	if snapshot.LocalAvailable {
		path := filepath.Join(c.cfg.DataDir, "snapshots", room.ID, fmt.Sprintf("%s-g%d.tar.gz", room.ID, room.Generation))
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		digest := sha256.New()
		size, err := io.Copy(digest, file)
		if err != nil {
			return err
		}
		if size != snapshot.Size || fmt.Sprintf("%x", digest.Sum(nil)) != snapshot.SHA256 {
			return fmt.Errorf("snapshot bytes differ from the recorded archive")
		}
	}
	name, err := objectName(room.ID)
	if err != nil {
		return err
	}
	observed, err := c.verifyCurrent(ctx, ref)
	if err != nil {
		return err
	}
	if observed.ProcessExists {
		return fmt.Errorf("room Pod still exists")
	}
	if observed.Exists && (observed.Generation != room.Generation || observed.BuildID != room.BuildID) {
		return fmt.Errorf("room generation changed before deletion")
	}
	job, err := c.api.BatchV1().Jobs(c.ns).Get(ctx, archiveJobName(room.ID, room.Generation), metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if !c.managed(job.Labels, room.ID) || job.Labels["vex.imdomestic.com/generation"] != strconv.FormatInt(room.Generation, 10) || job.Labels["vex.imdomestic.com/build"] != room.BuildID {
			return fmt.Errorf("archive Job identity mismatch")
		}
		complete := false
		for _, condition := range job.Status.Conditions {
			if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
				complete = true
			}
		}
		if !complete {
			return fmt.Errorf("archive Job has not completed")
		}
		ttl := int32(1)
		if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != ttl {
			job.Spec.TTLSecondsAfterFinished = &ttl
			if _, err = c.api.BatchV1().Jobs(c.ns).Update(ctx, job, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
	}
	if options.KeepWorld {
		return nil
	}
	pods, err := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{LabelSelector: "vex.imdomestic.com/room=" + room.ID})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
			return fmt.Errorf("room PVC is still used by a Pod")
		}
	}
	policy := metav1.DeletePropagationForeground
	workload, err := c.api.AppsV1().StatefulSets(c.ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if !c.managed(workload.Labels, room.ID) || workload.Labels["vex.imdomestic.com/generation"] != ref.Attributes["generation"] || workload.Labels["vex.imdomestic.com/build"] != room.BuildID {
			return fmt.Errorf("StatefulSet identity changed before deletion")
		}
		if err = c.api.AppsV1().StatefulSets(c.ns).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy, Preconditions: &metav1.Preconditions{UID: &workload.UID}}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	pvc, err := c.api.CoreV1().PersistentVolumeClaims(c.ns).Get(ctx, "data-"+name+"-0", metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if !c.managed(pvc.Labels, room.ID) {
			return fmt.Errorf("PVC is not owned by this room")
		}
		if err = c.api.CoreV1().PersistentVolumeClaims(c.ns).Delete(ctx, pvc.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pvc.UID}}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	for _, kind := range []string{"service", "secret"} {
		if kind == "service" {
			object, e := c.api.CoreV1().Services(c.ns).Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				continue
			}
			if e != nil {
				return e
			}
			if !c.managed(object.Labels, room.ID) {
				return fmt.Errorf("Service is not owned by this room")
			}
			if err = c.deleteWakeSlice(ctx, room.ID, object); err != nil {
				return err
			}
			err = c.api.CoreV1().Services(c.ns).Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &object.UID}})
		} else {
			object, e := c.api.CoreV1().Secrets(c.ns).Get(ctx, name+"-credentials", metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				continue
			}
			if e != nil {
				return e
			}
			if !c.managed(object.Labels, room.ID) {
				return fmt.Errorf("Secret is not owned by this room")
			}
			err = c.api.CoreV1().Secrets(c.ns).Delete(ctx, object.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &object.UID}})
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
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
if curl --fail-with-body --silent --show-error --request POST --header "Authorization: Bearer $VEX_ROOM_TOKEN" --header 'Content-Type: application/gzip' --data-binary "@$archive" "$UPLOAD_URL" --output /data/.work/snapshot-response.json; then
  exit 0
else
  status=$?
  if test -s /data/.work/snapshot-response.json; then
    jq --raw-output '.error.message // "snapshot upload failed"' /data/.work/snapshot-response.json >&2
  fi
  exit "$status"
fi
`

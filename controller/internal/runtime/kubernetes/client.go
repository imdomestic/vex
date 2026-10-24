package kubernetes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strconv"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/runtime"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/retry"
)

type Client struct {
	cfg  config.ProjectConfig
	rest *rest.Config
	api  *clientset.Clientset
	ns   string
}

type RoomSpec = runtime.RoomSpec
type RoomStatus = runtime.RoomStatus

func New(cfg config.ProjectConfig) (*Client, error) {
	namespace, err := projectNamespace(cfg.Runtime.Kubernetes, cfg.ID)
	if err != nil {
		return nil, err
	}
	rc, err := clientcmd.BuildConfigFromFlags("", cfg.Runtime.Kubernetes.KubeconfigFile)
	if err != nil {
		return nil, err
	}
	rc.UserAgent = "vexd"
	rc.QPS, rc.Burst = 20, 40
	api, err := clientset.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, rest: rc, api: api, ns: namespace}, nil
}

func objectName(id string) (string, error) {
	if len(id) > 63 || len(validation.IsDNS1123Label(id)) != 0 {
		return "", fmt.Errorf("invalid room ID")
	}
	if len(id) > 53 {
		digest := sha256.Sum256([]byte(id))
		return fmt.Sprintf("room-%s-%x", id[:40], digest[:6]), nil
	}
	return "room-" + id, nil
}

func (c *Client) managed(labels map[string]string, id string) bool {
	return labels["vex.imdomestic.com/managed"] == "true" && labels["vex.imdomestic.com/project"] == c.cfg.ID && labels["vex.imdomestic.com/room"] == id
}

func (c *Client) ensureRoom(ctx context.Context, spec RoomSpec) error {
	service, secret, workload, err := c.objects(spec)
	if err != nil {
		return err
	}
	previous, err := c.api.AppsV1().StatefulSets(c.ns).Get(ctx, workload.Name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if !c.managed(previous.Labels, spec.Room.ID) {
			return fmt.Errorf("StatefulSet is not owned by this room")
		}
		generation, e := strconv.ParseInt(previous.Labels["vex.imdomestic.com/generation"], 10, 64)
		if e != nil {
			return e
		}
		if generation > spec.Room.Generation || (generation == spec.Room.Generation && previous.Labels["vex.imdomestic.com/build"] != spec.Room.BuildID) {
			return fmt.Errorf("room generation or build identity conflicts")
		}
		if generation < spec.Room.Generation {
			pods, e := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{LabelSelector: "vex.imdomestic.com/room=" + spec.Room.ID + ",app.kubernetes.io/component=minecraft"})
			if e != nil {
				return e
			}
			if len(pods.Items) != 0 {
				return fmt.Errorf("previous room Pod still exists")
			}
		}
	}
	if err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, e := c.api.CoreV1().Secrets(c.ns).Get(ctx, secret.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			_, e = c.api.CoreV1().Secrets(c.ns).Create(ctx, secret, metav1.CreateOptions{})
			return e
		}
		if e != nil {
			return e
		}
		if !c.managed(current.Labels, spec.Room.ID) {
			return fmt.Errorf("credential Secret is not owned by this room")
		}
		updated := current.DeepCopy()
		updated.Labels = mergeManagedLabels(updated.Labels, secret.Labels)
		if updated.Data == nil {
			updated.Data = map[string][]byte{}
		}
		for key, value := range secret.Data {
			updated.Data[key] = value
		}
		updated.Type = secret.Type
		if equality.Semantic.DeepEqual(current, updated) {
			return nil
		}
		_, e = c.api.CoreV1().Secrets(c.ns).Update(ctx, updated, metav1.UpdateOptions{})
		return e
	}); err != nil {
		return err
	}
	if err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, e := c.api.CoreV1().Services(c.ns).Get(ctx, service.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			_, e = c.api.CoreV1().Services(c.ns).Create(ctx, service, metav1.CreateOptions{})
			return e
		}
		if e != nil {
			return e
		}
		if !c.managed(current.Labels, spec.Room.ID) {
			return fmt.Errorf("Service is not owned by this room")
		}
		before := current.DeepCopy()
		current.Labels = mergeManagedLabels(current.Labels, service.Labels)
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		for key, value := range service.Annotations {
			current.Annotations[key] = value
		}
		ports := append([]corev1.ServicePort(nil), service.Spec.Ports...)
		for i := range ports {
			for _, existing := range current.Spec.Ports {
				if existing.Name == ports[i].Name && existing.Protocol == ports[i].Protocol {
					ports[i].NodePort = existing.NodePort
				}
			}
		}
		current.Spec.Ports, current.Spec.Selector = ports, service.Spec.Selector
		wake, wakeErr := c.api.DiscoveryV1().EndpointSlices(c.ns).Get(ctx, service.Name+"-wake", metav1.GetOptions{})
		if wakeErr != nil && !apierrors.IsNotFound(wakeErr) {
			return wakeErr
		}
		if wakeErr == nil {
			if !c.wakeOwned(wake, spec.Room.ID, current) {
				return fmt.Errorf("wake EndpointSlice ownership conflicts")
			}
			current.Spec.Selector = nil
		}
		if equality.Semantic.DeepEqual(before, current) {
			return nil
		}
		_, e = c.api.CoreV1().Services(c.ns).Update(ctx, current, metav1.UpdateOptions{})
		return e
	}); err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, e := c.api.AppsV1().StatefulSets(c.ns).Get(ctx, workload.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			_, e = c.api.AppsV1().StatefulSets(c.ns).Create(ctx, workload, metav1.CreateOptions{})
			return e
		}
		if e != nil {
			return e
		}
		if !c.managed(current.Labels, spec.Room.ID) {
			return fmt.Errorf("StatefulSet is not owned by this room")
		}
		before := current.DeepCopy()
		generation, e := strconv.ParseInt(current.Labels["vex.imdomestic.com/generation"], 10, 64)
		if e != nil {
			return e
		}
		if generation > spec.Room.Generation {
			return fmt.Errorf("room generation is stale")
		}
		if generation < spec.Room.Generation {
			pods, e := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{LabelSelector: "vex.imdomestic.com/room=" + spec.Room.ID + ",app.kubernetes.io/component=minecraft"})
			if e != nil {
				return e
			}
			if len(pods.Items) != 0 {
				return fmt.Errorf("previous room Pod still exists")
			}
			current.Spec.Template = workload.Spec.Template
		}
		if generation == spec.Room.Generation && current.Labels["vex.imdomestic.com/build"] != spec.Room.BuildID {
			return fmt.Errorf("room build cannot change within a generation")
		}
		current.Labels = mergeManagedLabels(current.Labels, workload.Labels)
		current.Spec.Replicas = workload.Spec.Replicas
		if equality.Semantic.DeepEqual(before, current) {
			return nil
		}
		_, e = c.api.AppsV1().StatefulSets(c.ns).Update(ctx, current, metav1.UpdateOptions{})
		return e
	})
}

func mergeManagedLabels(current, desired map[string]string) map[string]string {
	if current == nil {
		current = map[string]string{}
	}
	for key, value := range desired {
		current[key] = value
	}
	return current
}

func (c *Client) syncHost(ctx context.Context, id, host string) error {
	name, err := objectName(id)
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		svc, e := c.api.CoreV1().Services(c.ns).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if !c.managed(svc.Labels, id) {
			return fmt.Errorf("Service is not owned by this room")
		}
		if svc.Annotations["mc-router.itzg.me/externalServerName"] == host {
			return nil
		}
		if svc.Annotations == nil {
			svc.Annotations = map[string]string{}
		}
		svc.Annotations["mc-router.itzg.me/externalServerName"] = host
		_, e = c.api.CoreV1().Services(c.ns).Update(ctx, svc, metav1.UpdateOptions{})
		return e
	})
}

func (c *Client) observeRoom(ctx context.Context, id string) (RoomStatus, error) {
	var status RoomStatus
	name, err := objectName(id)
	if err != nil {
		return status, err
	}
	sts, err := c.api.AppsV1().StatefulSets(c.ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return status, err
	}
	if err == nil {
		if !c.managed(sts.Labels, id) {
			return status, fmt.Errorf("StatefulSet is not owned by this room")
		}
		status.Exists = true
		status.Generation, err = strconv.ParseInt(sts.Labels["vex.imdomestic.com/generation"], 10, 64)
		if err != nil {
			return status, err
		}
		status.BuildID = sts.Labels["vex.imdomestic.com/build"]
	}
	pod, err := c.api.CoreV1().Pods(c.ns).Get(ctx, name+"-0", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		status.Phase = "Pod absent"
		return status, nil
	}
	if err != nil {
		return status, err
	}
	if !c.managed(pod.Labels, id) {
		return status, fmt.Errorf("Pod is not owned by this room")
	}
	if !status.Exists {
		status.Generation, err = strconv.ParseInt(pod.Labels["vex.imdomestic.com/generation"], 10, 64)
		if err != nil || status.Generation < 1 || pod.Labels["vex.imdomestic.com/build"] == "" {
			return status, fmt.Errorf("orphan Pod has invalid runtime identity")
		}
		status.BuildID = pod.Labels["vex.imdomestic.com/build"]
	}
	status.ProcessExists, status.Node = true, pod.Spec.NodeName
	status.Phase, status.Message = string(pod.Status.Phase), pod.Status.Message
	status.IdentityMatches = pod.Labels["vex.imdomestic.com/generation"] == strconv.FormatInt(status.Generation, 10) && pod.Labels["vex.imdomestic.com/build"] == status.BuildID
	status.Terminating = pod.DeletionTimestamp != nil
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			status.ProcessReady = condition.Status == corev1.ConditionTrue
			status.Ready = status.IdentityMatches && !status.Terminating && status.ProcessReady
			if condition.Status != corev1.ConditionTrue {
				status.Message = condition.Message
			}
		}
	}
	for _, container := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		if container.State.Waiting != nil {
			status.Phase = container.State.Waiting.Reason
			status.Message = container.State.Waiting.Message
		}
		if container.State.Terminated != nil && container.State.Terminated.ExitCode != 0 {
			status.Phase = container.State.Terminated.Reason
			status.Message = container.State.Terminated.Message
		}
	}
	return status, nil
}

func (c *Client) scaleRoom(ctx context.Context, id string, replicas int32) error {
	if replicas != 0 && replicas != 1 {
		return fmt.Errorf("room replicas must be zero or one")
	}
	name, err := objectName(id)
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		sts, e := c.api.AppsV1().StatefulSets(c.ns).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(e) && replicas == 0 {
			return nil
		}
		if e != nil {
			return e
		}
		if !c.managed(sts.Labels, id) {
			return fmt.Errorf("StatefulSet is not owned by this room")
		}
		if sts.Spec.Replicas != nil && *sts.Spec.Replicas == replicas {
			return nil
		}
		sts.Spec.Replicas = &replicas
		_, e = c.api.AppsV1().StatefulSets(c.ns).Update(ctx, sts, metav1.UpdateOptions{})
		return e
	})
}

func (c *Client) rcon(ctx context.Context, id, command string) (string, error) {
	name, err := objectName(id)
	if err != nil {
		return "", err
	}
	pod, err := c.api.CoreV1().Pods(c.ns).Get(ctx, name+"-0", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if !c.managed(pod.Labels, id) {
		return "", fmt.Errorf("Pod is not owned by this room")
	}
	return c.executePod(ctx, pod.Name, []string{"rcon-cli", command})
}

func (c *Client) executePod(ctx context.Context, pod string, command []string) (string, error) {
	req := c.api.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(c.ns).SubResource("exec").VersionedParams(&corev1.PodExecOptions{
		Container: "mc", Command: command, Stdout: true, Stderr: true,
	}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(c.rest, "POST", req.URL())
	if err != nil {
		return "", err
	}
	var out, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &out, Stderr: &stderr})
	if err != nil {
		return out.String(), fmt.Errorf("Pod exec: %w: %s", err, stderr.String())
	}
	return out.String(), nil
}

func (c *Client) logs(ctx context.Context, id string, tail int64) (string, error) {
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
	if !c.managed(pod.Labels, id) {
		return "", fmt.Errorf("Pod is not owned by this room")
	}
	stream, err := c.api.CoreV1().Pods(c.ns).GetLogs(pod.Name, &corev1.PodLogOptions{Container: "mc", TailLines: &tail, Timestamps: true}).Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, 2<<20))
	return string(data), err
}

func (c *Client) Capacity(ctx context.Context) ([]model.Node, error) {
	nodes, err := c.api.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	rooms, err := c.api.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: "vex.imdomestic.com/managed=true,app.kubernetes.io/component=minecraft"})
	if err != nil {
		return nil, err
	}
	workloads, err := c.api.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{LabelSelector: "vex.imdomestic.com/managed=true"})
	if err != nil {
		return nil, err
	}
	result := []model.Node{}
	for _, node := range nodes.Items {
		configured, exists := c.nodeConfig(node.Name)
		if !exists {
			continue
		}
		n := model.Node{Name: node.Name, Roles: append([]string{}, configured.Roles...), Priority: configured.Priority, Schedulable: roomNodeSchedulable(node, roomTolerations()), MaxRunning: configured.MaxRooms, Rooms: []string{}}
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				n.Ready = condition.Status == corev1.ConditionTrue
			}
		}
		for _, pod := range rooms.Items {
			project := pod.Labels["vex.imdomestic.com/project"]
			namespace, namespaceErr := projectNamespace(c.cfg.Runtime.Kubernetes, project)
			if namespaceErr == nil && pod.Spec.NodeName == node.Name && pod.Namespace == namespace && pod.Labels["vex.imdomestic.com/room"] != "" && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
				n.Rooms = append(n.Rooms, project+"/"+pod.Labels["vex.imdomestic.com/room"])
			}
		}
		for _, workload := range workloads.Items {
			project, room := workload.Labels["vex.imdomestic.com/project"], workload.Labels["vex.imdomestic.com/room"]
			namespace, namespaceErr := projectNamespace(c.cfg.Runtime.Kubernetes, project)
			if namespaceErr != nil || room == "" || workload.Namespace != namespace || workload.Spec.Replicas == nil || *workload.Spec.Replicas == 0 || workload.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"] != node.Name {
				continue
			}
			identity := project + "/" + room
			present := false
			for _, existing := range n.Rooms {
				present = present || existing == identity
			}
			if !present {
				n.Rooms = append(n.Rooms, identity)
			}
		}
		if n.Ready {
			data, e := c.api.CoreV1().RESTClient().Get().Resource("nodes").Name(node.Name).SubResource("proxy").Suffix("stats/summary").DoRaw(ctx)
			if e != nil {
				return nil, fmt.Errorf("node %s filesystem statistics: %w", node.Name, e)
			}
			var summary struct {
				Node struct {
					FS struct {
						AvailableBytes *int64 `json:"availableBytes"`
					} `json:"fs"`
				} `json:"node"`
			}
			if e = json.Unmarshal(data, &summary); e != nil {
				return nil, e
			}
			if summary.Node.FS.AvailableBytes == nil || *summary.Node.FS.AvailableBytes < 0 {
				return nil, fmt.Errorf("node %s omitted valid filesystem availability", node.Name)
			}
			n.RootFreeBytes = summary.Node.FS.AvailableBytes
		}
		sort.Strings(n.Rooms)
		result = append(result, n)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (c *Client) RouterSource(ctx context.Context, source string) (bool, error) {
	address, err := netip.ParseAddr(source)
	if err != nil {
		return false, err
	}
	pods, err := c.api.CoreV1().Pods(c.cfg.Runtime.Kubernetes.SystemNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=mc-router,app.kubernetes.io/part-of=vex"})
	if err != nil {
		return false, err
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || pod.Spec.ServiceAccountName != "mc-router" {
			continue
		}
		for _, value := range pod.Status.PodIPs {
			candidate, e := netip.ParseAddr(value.IP)
			if e != nil {
				return false, e
			}
			if candidate.Unmap() == address.Unmap() {
				return true, nil
			}
		}
	}
	return false, nil
}

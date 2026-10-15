package kube

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/retry"
)

type Client struct {
	cfg  config.Config
	rest *rest.Config
	api  *kubernetes.Clientset
	ns   string
}

type RoomSpec struct {
	Room             model.Room  `json:"room"`
	Build            model.Build `json:"build"`
	Node             string      `json:"node"`
	Mods             []string    `json:"mods"`
	SeedURL          string      `json:"seedURL"`
	SeedSHA256       string      `json:"seedSHA256"`
	RoomToken        string      `json:"roomToken"`
	RCONPassword     string      `json:"rconPassword"`
	ResourcePackURL  string      `json:"resourcePackURL"`
	ResourcePackSHA1 string      `json:"resourcePackSHA1"`
}

type RoomStatus struct {
	Exists          bool
	PodExists       bool
	Ready           bool
	PodReady        bool
	IdentityMatches bool
	Terminating     bool
	Node            string
	BuildID         string
	Generation      int64
	Phase           string
	Message         string
}

func New(cfg config.Config) (*Client, error) {
	if cfg.Rooms.Namespace != "chorus-rooms" && !strings.HasPrefix(cfg.Rooms.Namespace, "chorus-it-") {
		return nil, fmt.Errorf("unsupported room namespace %q", cfg.Rooms.Namespace)
	}
	rc, err := clientcmd.BuildConfigFromFlags("", cfg.Rooms.Kubeconfig)
	if err != nil {
		return nil, err
	}
	rc.UserAgent = "chorusd"
	rc.QPS, rc.Burst = 20, 40
	api, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, rest: rc, api: api, ns: cfg.Rooms.Namespace}, nil
}

func objectName(id string) (string, error) {
	if len(id) > 40 || len(validation.IsDNS1123Label(id)) != 0 {
		return "", fmt.Errorf("invalid room ID")
	}
	return "room-" + id, nil
}

func managed(labels map[string]string, id string) bool {
	return labels["chorus.dev/managed"] == "true" && labels["chorus.dev/room"] == id
}

func (c *Client) EnsureRoom(ctx context.Context, spec RoomSpec) error {
	service, secret, workload, err := c.objects(spec)
	if err != nil {
		return err
	}
	previous, err := c.api.AppsV1().StatefulSets(c.ns).Get(ctx, workload.Name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if !managed(previous.Labels, spec.Room.ID) {
			return fmt.Errorf("StatefulSet is not owned by this room")
		}
		generation, e := strconv.ParseInt(previous.Labels["chorus.dev/generation"], 10, 64)
		if e != nil {
			return e
		}
		if generation > spec.Room.Generation || (generation == spec.Room.Generation && previous.Labels["chorus.dev/build"] != spec.Room.BuildID) {
			return fmt.Errorf("room generation or build identity conflicts")
		}
		if generation < spec.Room.Generation {
			pods, e := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{LabelSelector: "chorus.dev/room=" + spec.Room.ID + ",app.kubernetes.io/component=minecraft"})
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
		if !managed(current.Labels, spec.Room.ID) {
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
		if !managed(current.Labels, spec.Room.ID) {
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
		if !managed(current.Labels, spec.Room.ID) {
			return fmt.Errorf("StatefulSet is not owned by this room")
		}
		before := current.DeepCopy()
		generation, e := strconv.ParseInt(current.Labels["chorus.dev/generation"], 10, 64)
		if e != nil {
			return e
		}
		if generation > spec.Room.Generation {
			return fmt.Errorf("room generation is stale")
		}
		if generation < spec.Room.Generation {
			pods, e := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{LabelSelector: "chorus.dev/room=" + spec.Room.ID + ",app.kubernetes.io/component=minecraft"})
			if e != nil {
				return e
			}
			if len(pods.Items) != 0 {
				return fmt.Errorf("previous room Pod still exists")
			}
			current.Spec.Template = workload.Spec.Template
		}
		if generation == spec.Room.Generation && current.Labels["chorus.dev/build"] != spec.Room.BuildID {
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

func (c *Client) SyncHost(ctx context.Context, id, host string) error {
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
		if !managed(svc.Labels, id) {
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

func (c *Client) ObserveRoom(ctx context.Context, id string) (RoomStatus, error) {
	var status RoomStatus
	name, err := objectName(id)
	if err != nil {
		return status, err
	}
	sts, err := c.api.AppsV1().StatefulSets(c.ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	if !managed(sts.Labels, id) {
		return status, fmt.Errorf("StatefulSet is not owned by this room")
	}
	status.Exists = true
	status.Generation, err = strconv.ParseInt(sts.Labels["chorus.dev/generation"], 10, 64)
	if err != nil {
		return status, err
	}
	status.BuildID = sts.Labels["chorus.dev/build"]
	pod, err := c.api.CoreV1().Pods(c.ns).Get(ctx, name+"-0", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		status.Phase = "Pod absent"
		return status, nil
	}
	if err != nil {
		return status, err
	}
	if !managed(pod.Labels, id) {
		return status, fmt.Errorf("Pod is not owned by this room")
	}
	status.PodExists, status.Node = true, pod.Spec.NodeName
	status.Phase, status.Message = string(pod.Status.Phase), pod.Status.Message
	status.IdentityMatches = pod.Labels["chorus.dev/generation"] == strconv.FormatInt(status.Generation, 10) && pod.Labels["chorus.dev/build"] == status.BuildID
	status.Terminating = pod.DeletionTimestamp != nil
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			status.PodReady = condition.Status == corev1.ConditionTrue
			status.Ready = status.IdentityMatches && !status.Terminating && status.PodReady
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

func (c *Client) ScaleRoom(ctx context.Context, id string, replicas int32) error {
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
		if !managed(sts.Labels, id) {
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

func (c *Client) RCON(ctx context.Context, id, command string) (string, error) {
	name, err := objectName(id)
	if err != nil {
		return "", err
	}
	pod, err := c.api.CoreV1().Pods(c.ns).Get(ctx, name+"-0", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if !managed(pod.Labels, id) {
		return "", fmt.Errorf("Pod is not owned by this room")
	}
	req := c.api.CoreV1().RESTClient().Post().Resource("pods").Name(pod.Name).Namespace(c.ns).SubResource("exec").VersionedParams(&corev1.PodExecOptions{
		Container: "mc", Command: []string{"rcon-cli", command}, Stdout: true, Stderr: true,
	}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(c.rest, "POST", req.URL())
	if err != nil {
		return "", err
	}
	var out, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &out, Stderr: &stderr})
	if err != nil {
		return out.String(), fmt.Errorf("RCON exec: %w: %s", err, stderr.String())
	}
	return out.String(), nil
}

func (c *Client) Logs(ctx context.Context, id string, tail int64) (string, error) {
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
	stream, err := c.api.CoreV1().Pods(c.ns).GetLogs(pod.Name, &corev1.PodLogOptions{Container: "mc", TailLines: &tail, Timestamps: true}).Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, 2<<20))
	return string(data), err
}

func (c *Client) Nodes(ctx context.Context) ([]model.Node, error) {
	nodes, err := c.api.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	rooms, err := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{LabelSelector: "chorus.dev/managed=true"})
	if err != nil {
		return nil, err
	}
	result := []model.Node{}
	for _, node := range nodes.Items {
		n := model.Node{Name: node.Name, Schedulable: !node.Spec.Unschedulable, MaxRunning: c.cfg.Rooms.MaxRunning[node.Name], Rooms: []string{}}
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				n.Ready = condition.Status == corev1.ConditionTrue
			}
		}
		for _, pod := range rooms.Items {
			if pod.Spec.NodeName == node.Name && pod.Labels["chorus.dev/room"] != "" && pod.Labels["app.kubernetes.io/component"] == "minecraft" {
				n.Rooms = append(n.Rooms, pod.Labels["chorus.dev/room"])
			}
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
	pods, err := c.api.CoreV1().Pods("chorus-system").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=mc-router,app.kubernetes.io/part-of=chorus-testbed"})
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

package kubernetes

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/router"
	"github.com/imdomestic/vex/controller/internal/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

var _ runtime.Backend = (*Client)(nil)
var _ router.Registrar = (*Client)(nil)

func (c *Client) Reference(room model.Room) (runtime.ResourceRef, error) {
	namespace, err := projectNamespace(c.cfg.Runtime.Kubernetes, c.cfg.ID)
	if err != nil || namespace != c.ns {
		return runtime.ResourceRef{}, fmt.Errorf("invalid project namespace")
	}
	if room.Project != c.cfg.ID || room.Generation < 1 || room.BuildID == "" || len(room.ID)+2+len(c.cfg.ID) > 63 || strings.Contains(room.ID, "--") {
		return runtime.ResourceRef{}, fmt.Errorf("room project or runtime identity is invalid")
	}
	name, err := objectName(room.ID)
	if err != nil {
		return runtime.ResourceRef{}, err
	}
	return runtime.ResourceRef{ID: room.ID, WorldID: c.ns + "/data-" + name + "-0", Attributes: map[string]string{
		"project": c.cfg.ID, "namespace": c.ns, "objectName": name,
		"generation": strconv.FormatInt(room.Generation, 10), "build": room.BuildID, "kind": room.Kind,
	}}, nil
}

func (c *Client) referenceRoom(ref runtime.ResourceRef) (model.Room, error) {
	namespace, err := projectNamespace(c.cfg.Runtime.Kubernetes, c.cfg.ID)
	if err != nil || namespace != c.ns {
		return model.Room{}, fmt.Errorf("invalid project namespace")
	}
	name, err := objectName(ref.ID)
	if err != nil {
		return model.Room{}, err
	}
	a := ref.Attributes
	if a["project"] != c.cfg.ID || a["namespace"] != c.ns || a["objectName"] != name || ref.WorldID != c.ns+"/data-"+name+"-0" {
		return model.Room{}, fmt.Errorf("resource reference belongs to a different project or object")
	}
	generation, err := strconv.ParseInt(a["generation"], 10, 64)
	if err != nil || generation < 1 || a["build"] == "" {
		return model.Room{}, fmt.Errorf("invalid resource identity")
	}
	if a["kind"] != "branch" && a["kind"] != "commit" && a["kind"] != "world" {
		return model.Room{}, fmt.Errorf("invalid resource kind")
	}
	return model.Room{Project: c.cfg.ID, ID: ref.ID, BuildID: a["build"], Generation: generation, Kind: a["kind"]}, nil
}

func (c *Client) EnsureRoom(ctx context.Context, spec runtime.RoomSpec) (runtime.ResourceRef, error) {
	ref, err := c.Reference(spec.Room)
	if err != nil {
		return runtime.ResourceRef{}, err
	}
	if err = c.ensureRoom(ctx, spec); err != nil {
		return runtime.ResourceRef{}, err
	}
	return ref, nil
}

func (c *Client) ObserveRoom(ctx context.Context, ref runtime.ResourceRef) (runtime.RoomStatus, error) {
	room, err := c.referenceRoom(ref)
	if err != nil {
		return runtime.RoomStatus{}, err
	}
	status, err := c.observeRoom(ctx, room.ID)
	if err != nil {
		return status, err
	}
	status.IdentityMatches = status.IdentityMatches && status.Generation == room.Generation && status.BuildID == room.BuildID
	status.Ready = status.Ready && status.IdentityMatches
	return status, nil
}

func (c *Client) verifyCurrent(ctx context.Context, ref runtime.ResourceRef) (runtime.RoomStatus, error) {
	room, err := c.referenceRoom(ref)
	if err != nil {
		return runtime.RoomStatus{}, err
	}
	status, err := c.observeRoom(ctx, room.ID)
	if err != nil {
		return status, err
	}
	if (status.Exists || status.ProcessExists) && (status.Generation != room.Generation || status.BuildID != room.BuildID) {
		return status, fmt.Errorf("resource generation or build identity changed")
	}
	if status.ProcessExists && !status.IdentityMatches {
		return status, fmt.Errorf("running process identity does not match resource")
	}
	return status, nil
}

func (c *Client) StopRoom(ctx context.Context, ref runtime.ResourceRef, options runtime.StopOptions) error {
	if options.Timeout <= 0 {
		return fmt.Errorf("stop timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	status, err := c.verifyCurrent(ctx, ref)
	if err != nil {
		return err
	}
	if options.Save && status.ProcessReady && !status.Terminating {
		if _, err = c.rcon(ctx, ref.ID, "save-all flush"); err != nil {
			return err
		}
	}
	if !status.Exists && status.ProcessExists {
		pod, err := c.api.CoreV1().Pods(c.ns).Get(ctx, ref.Attributes["objectName"]+"-0", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !c.managed(pod.Labels, ref.ID) || pod.Labels["vex.imdomestic.com/generation"] != ref.Attributes["generation"] || pod.Labels["vex.imdomestic.com/build"] != ref.Attributes["build"] {
			return fmt.Errorf("orphan Pod identity changed before stopping")
		}
		return c.api.CoreV1().Pods(c.ns).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}})
	}
	// 镜像的终止处理保存世界；缩容后由调用方观察进程实际退出。
	return c.scaleRoom(ctx, ref.ID, 0)
}

func (c *Client) RCON(ctx context.Context, ref runtime.ResourceRef, command string) (string, error) {
	status, err := c.verifyCurrent(ctx, ref)
	if err != nil {
		return "", err
	}
	if !status.ProcessExists || status.Terminating {
		return "", fmt.Errorf("room process is unavailable")
	}
	return c.rcon(ctx, ref.ID, command)
}

func (c *Client) Logs(ctx context.Context, ref runtime.ResourceRef, options runtime.LogOptions) (string, error) {
	if _, err := c.referenceRoom(ref); err != nil {
		return "", err
	}
	if options.Diagnostics {
		output, err := c.failureLogs(ctx, ref.ID, options.Tail)
		if err != nil {
			return "", err
		}
		lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
		if int64(len(lines)) > options.Tail {
			lines = lines[len(lines)-int(options.Tail):]
		}
		return strings.Join(lines, "\n"), nil
	}
	return c.logs(ctx, ref.ID, options.Tail)
}

func (c *Client) Sync(ctx context.Context, ref runtime.ResourceRef, host string) error {
	if _, err := c.referenceRoom(ref); err != nil {
		return err
	}
	if len(validation.IsDNS1123Subdomain(host)) != 0 || host != ref.ID+"--"+c.cfg.ID+"."+c.cfg.Server.Zone {
		return fmt.Errorf("route hostname does not belong to this project room")
	}
	return c.syncHost(ctx, ref.ID, host)
}

func (c *Client) Remove(ctx context.Context, ref runtime.ResourceRef) error {
	if _, err := c.referenceRoom(ref); err != nil {
		return err
	}
	svc, err := c.api.CoreV1().Services(c.ns).Get(ctx, ref.Attributes["objectName"], metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !c.managed(svc.Labels, ref.ID) {
		return fmt.Errorf("route Service ownership mismatch")
	}
	if _, exists := svc.Annotations["mc-router.itzg.me/externalServerName"]; !exists {
		return nil
	}
	delete(svc.Annotations, "mc-router.itzg.me/externalServerName")
	_, err = c.api.CoreV1().Services(c.ns).Update(ctx, svc, metav1.UpdateOptions{})
	return err
}

const DefaultStopTimeout = 120 * time.Second

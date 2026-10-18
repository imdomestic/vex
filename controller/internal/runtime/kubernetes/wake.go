package kubernetes

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/router"
	"github.com/imdomestic/vex/controller/internal/runtime"
	"github.com/imdomestic/vex/controller/internal/slp"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/util/retry"
)

const wakeManager = "wake.vex.imdomestic.com"

var _ router.WakeRegistrar = (*Client)(nil)

func (c *Client) Sleep(ctx context.Context, room model.Room, host, endpoint string) error {
	if room.Project != c.cfg.ID || room.Generation < 0 || room.Kind != "branch" && room.Kind != "pr" || (room.State != "stopped" && room.State != "failed") || host != room.ID+"--"+c.cfg.ID+"."+c.cfg.Server.Zone || len(validation.IsDNS1123Subdomain(host)) != 0 {
		return fmt.Errorf("sleeping route identity or state is invalid")
	}
	address, err := netip.ParseAddrPort(endpoint)
	if err != nil || !address.Addr().IsGlobalUnicast() || address.Addr().IsLoopback() || address.Port() == 0 || address.Addr().Zone() != "" {
		return fmt.Errorf("wake endpoint must be a reachable unicast IP and port")
	}
	name, err := objectName(room.ID)
	if err != nil {
		return err
	}
	if err = c.requireSleeping(ctx, room); err != nil {
		return err
	}
	labels := map[string]string{"vex.imdomestic.com/managed": "true", "vex.imdomestic.com/project": c.cfg.ID, "vex.imdomestic.com/room": room.ID, "vex.imdomestic.com/generation": strconv.FormatInt(room.Generation, 10), "vex.imdomestic.com/build": room.BuildID, "vex.imdomestic.com/kind": room.Kind}
	family, addressType := corev1.IPv4Protocol, discoveryv1.AddressTypeIPv4
	if address.Addr().Is6() {
		family, addressType = corev1.IPv6Protocol, discoveryv1.AddressTypeIPv6
	}
	var service *corev1.Service
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := c.api.CoreV1().Services(c.ns).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			service, err = c.api.CoreV1().Services(c.ns).Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.ns, Labels: labels, Annotations: map[string]string{"mc-router.itzg.me/externalServerName": host, "mc-router.itzg.me/autoScaleUp": "false", "mc-router.itzg.me/autoScaleDown": "false"}}, Spec: corev1.ServiceSpec{IPFamilies: []corev1.IPFamily{family}, Ports: []corev1.ServicePort{{Name: "minecraft", Protocol: corev1.ProtocolTCP, Port: 25565, TargetPort: intstr.FromInt32(25565)}}}}, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		if !c.managed(current.Labels, room.ID) || current.DeletionTimestamp != nil || len(current.Spec.IPFamilies) != 1 || current.Spec.IPFamilies[0] != family {
			return fmt.Errorf("sleeping route Service ownership or address family conflicts")
		}
		generation, err := strconv.ParseInt(current.Labels["vex.imdomestic.com/generation"], 10, 64)
		if err != nil || generation > room.Generation || generation == room.Generation && room.Generation > 0 && current.Labels["vex.imdomestic.com/build"] != room.BuildID {
			return fmt.Errorf("sleeping route Service identity conflicts")
		}
		if err = c.requireSleeping(ctx, room); err != nil {
			return err
		}
		before := current.DeepCopy()
		current.Spec.Selector = nil
		current.Labels = mergeManagedLabels(current.Labels, labels)
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		current.Annotations["mc-router.itzg.me/externalServerName"] = host
		current.Annotations["mc-router.itzg.me/autoScaleUp"] = "false"
		current.Annotations["mc-router.itzg.me/autoScaleDown"] = "false"
		service = current
		if equality.Semantic.DeepEqual(before, current) {
			return nil
		}
		service, err = c.api.CoreV1().Services(c.ns).Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return err
	}
	port, portName, protocol, ready := int32(address.Port()), "minecraft", corev1.ProtocolTCP, true
	desired := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: name + "-wake", Namespace: c.ns, Labels: map[string]string{
		"vex.imdomestic.com/managed": "true", "vex.imdomestic.com/project": c.cfg.ID, "vex.imdomestic.com/room": room.ID,
		discoveryv1.LabelServiceName: service.Name, discoveryv1.LabelManagedBy: wakeManager,
	}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: service.Name, UID: service.UID}}}, AddressType: addressType, Ports: []discoveryv1.EndpointPort{{Name: &portName, Protocol: &protocol, Port: &port}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{address.Addr().String()}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}}}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := c.api.DiscoveryV1().EndpointSlices(c.ns).Get(ctx, desired.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = c.api.DiscoveryV1().EndpointSlices(c.ns).Create(ctx, desired, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		if !c.wakeOwned(current, room.ID, service) || current.DeletionTimestamp != nil {
			return fmt.Errorf("wake EndpointSlice ownership conflicts")
		}
		before := current.DeepCopy()
		current.AddressType, current.Ports, current.Endpoints = desired.AddressType, desired.Ports, desired.Endpoints
		if equality.Semantic.DeepEqual(before, current) {
			return nil
		}
		_, err = c.api.DiscoveryV1().EndpointSlices(c.ns).Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

func (c *Client) requireSleeping(ctx context.Context, room model.Room) error {
	name, err := objectName(room.ID)
	if err != nil {
		return err
	}
	workload, err := c.api.AppsV1().StatefulSets(c.ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		generation, parseErr := strconv.ParseInt(workload.Labels["vex.imdomestic.com/generation"], 10, 64)
		if !c.managed(workload.Labels, room.ID) || parseErr != nil || generation > room.Generation || workload.Spec.Replicas == nil || *workload.Spec.Replicas != 0 {
			return fmt.Errorf("sleeping route cannot replace an active workload")
		}
	}
	pods, err := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{LabelSelector: "vex.imdomestic.com/room=" + room.ID + ",app.kubernetes.io/component=minecraft"})
	if err != nil {
		return err
	}
	if len(pods.Items) != 0 {
		return fmt.Errorf("sleeping route cannot replace an existing Minecraft process")
	}
	return nil
}

func (c *Client) wakeOwned(slice *discoveryv1.EndpointSlice, room string, service *corev1.Service) bool {
	if !c.managed(slice.Labels, room) || slice.Labels[discoveryv1.LabelManagedBy] != wakeManager || slice.Labels[discoveryv1.LabelServiceName] != service.Name {
		return false
	}
	return len(slice.OwnerReferences) == 1 && slice.OwnerReferences[0].APIVersion == "v1" && slice.OwnerReferences[0].Kind == "Service" && slice.OwnerReferences[0].UID == service.UID && slice.OwnerReferences[0].Name == service.Name
}

func (c *Client) deleteWakeSlice(ctx context.Context, room string, service *corev1.Service) error {
	slice, err := c.api.DiscoveryV1().EndpointSlices(c.ns).Get(ctx, service.Name+"-wake", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !c.wakeOwned(slice, room, service) {
		return fmt.Errorf("wake EndpointSlice ownership conflicts")
	}
	err = c.api.DiscoveryV1().EndpointSlices(c.ns).Delete(ctx, slice.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &slice.UID}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (c *Client) Restore(ctx context.Context, ref runtime.ResourceRef, host string) error {
	room, err := c.referenceRoom(ref)
	if err != nil {
		return err
	}
	if host != ref.ID+"--"+c.cfg.ID+"."+c.cfg.Server.Zone || len(validation.IsDNS1123Subdomain(host)) != 0 {
		return fmt.Errorf("route hostname does not belong to this project room")
	}
	status, err := c.verifyCurrent(ctx, ref)
	if err != nil {
		return err
	}
	if !status.Ready {
		return fmt.Errorf("route cannot be restored before the matching process is ready")
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		service, err := c.api.CoreV1().Services(c.ns).Get(ctx, ref.Attributes["objectName"], metav1.GetOptions{})
		if err != nil {
			return err
		}
		if !c.managed(service.Labels, ref.ID) || service.DeletionTimestamp != nil || service.Labels["vex.imdomestic.com/generation"] != ref.Attributes["generation"] || service.Labels["vex.imdomestic.com/build"] != ref.Attributes["build"] {
			return fmt.Errorf("route Service identity conflicts")
		}
		if len(service.Spec.Selector) == 0 {
			pod, err := c.api.CoreV1().Pods(c.ns).Get(ctx, ref.Attributes["objectName"]+"-0", metav1.GetOptions{})
			if err != nil {
				return err
			}
			if !c.managed(pod.Labels, ref.ID) || pod.DeletionTimestamp != nil || pod.Labels["vex.imdomestic.com/generation"] != ref.Attributes["generation"] || pod.Labels["vex.imdomestic.com/build"] != ref.Attributes["build"] {
				return fmt.Errorf("route Pod identity changed before status verification")
			}
			source, err := c.probeSource(ctx, pod.Status.PodIP)
			if err != nil {
				return err
			}
			probe, err := slp.ProbeFrom(ctx, net.JoinHostPort(pod.Status.PodIP, "25565"), host, 25565, source)
			if err != nil {
				return fmt.Errorf("room status verification before route restoration: %w", err)
			}
			if !slp.Matches(probe, room) {
				return fmt.Errorf("room status identity differs before route restoration")
			}
		}
		if err = c.deleteWakeSlice(ctx, ref.ID, service); err != nil {
			return err
		}
		before := service.DeepCopy()
		service.Spec.Selector = map[string]string{"vex.imdomestic.com/managed": "true", "vex.imdomestic.com/project": c.cfg.ID, "vex.imdomestic.com/room": ref.ID, "app.kubernetes.io/component": "minecraft"}
		if service.Annotations == nil {
			service.Annotations = map[string]string{}
		}
		service.Annotations["mc-router.itzg.me/externalServerName"] = host
		if equality.Semantic.DeepEqual(before, service) {
			return nil
		}
		_, err = c.api.CoreV1().Services(c.ns).Update(ctx, service, metav1.UpdateOptions{})
		return err
	})
}

func (c *Client) probeSource(ctx context.Context, podIP string) (net.IP, error) {
	target, err := netip.ParseAddr(podIP)
	if err != nil || !target.IsGlobalUnicast() {
		return nil, fmt.Errorf("room Pod has no routable address")
	}
	endpoint, err := url.Parse(c.cfg.Server.InternalBaseURL)
	if err != nil || endpoint.Hostname() == "" {
		return nil, fmt.Errorf("invalid internal API endpoint")
	}
	candidates, err := net.DefaultResolver.LookupNetIP(ctx, "ip", endpoint.Hostname())
	if err != nil {
		return nil, err
	}
	interfaces, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		candidate = candidate.Unmap()
		if !candidate.IsGlobalUnicast() || candidate.Is4() != target.Unmap().Is4() {
			continue
		}
		for _, local := range interfaces {
			prefix, err := netip.ParsePrefix(local.String())
			if err != nil {
				return nil, err
			}
			if prefix.Addr().Unmap() == candidate {
				return net.IP(candidate.AsSlice()), nil
			}
		}
	}
	return nil, fmt.Errorf("internal API endpoint has no local address matching the Pod address family")
}

func (c *Client) Forget(ctx context.Context, room model.Room) error {
	if room.Project != c.cfg.ID || room.State != "closed" || room.Generation < 0 {
		return fmt.Errorf("only a closed project room can remove its sleeping route")
	}
	if err := c.requireSleeping(ctx, room); err != nil {
		return err
	}
	name, err := objectName(room.ID)
	if err != nil {
		return err
	}
	service, err := c.api.CoreV1().Services(c.ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !c.managed(service.Labels, room.ID) || service.Labels["vex.imdomestic.com/generation"] != strconv.FormatInt(room.Generation, 10) || service.Labels["vex.imdomestic.com/build"] != room.BuildID {
		return fmt.Errorf("closed room route identity or selector conflicts")
	}
	if err = c.deleteWakeSlice(ctx, room.ID, service); err != nil {
		return err
	}
	err = c.api.CoreV1().Services(c.ns).Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &service.UID, ResourceVersion: &service.ResourceVersion}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

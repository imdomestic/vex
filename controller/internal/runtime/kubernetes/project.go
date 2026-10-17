package kubernetes

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/imdomestic/vex/controller/internal/config"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/util/retry"
)

func projectNamespace(cfg config.Kubernetes, project string) (string, error) {
	prefix := cfg.NamespacePrefix
	if !config.ValidID(project) || !strings.HasSuffix(prefix, "-") || len(validation.IsDNS1123Label(strings.TrimSuffix(prefix, "-"))) != 0 {
		return "", fmt.Errorf("invalid project namespace prefix or project ID")
	}
	namespace := prefix + project
	if len(validation.IsDNS1123Label(namespace)) != 0 || len(validation.IsDNS1123Label(cfg.SystemNamespace)) != 0 || namespace == cfg.SystemNamespace {
		return "", fmt.Errorf("invalid project or system namespace")
	}
	return namespace, nil
}

func (c *Client) nodeConfig(name string) (config.Node, bool) {
	for _, node := range c.cfg.Runtime.Kubernetes.Nodes {
		if node.K8sName == name {
			return node, true
		}
	}
	return config.Node{}, false
}

func (c *Client) nodeAllows(name, kind string) bool {
	node, exists := c.nodeConfig(name)
	role := "ephemeral"
	if kind == "branch" {
		role = "persistent"
	}
	return exists && node.MaxRooms > 0 && slices.Contains(node.Roles, role)
}

func (c *Client) projectOwned(labels map[string]string) bool {
	return labels["vex.imdomestic.com/managed"] == "true" && labels["vex.imdomestic.com/project"] == c.cfg.ID
}

func (c *Client) EnsureProject(ctx context.Context) error {
	namespace, err := projectNamespace(c.cfg.Runtime.Kubernetes, c.cfg.ID)
	if err != nil || c.ns != namespace {
		return fmt.Errorf("invalid project namespace")
	}
	quota, limits, policy, err := c.projectObjects(ctx)
	if err != nil {
		return err
	}
	labels := map[string]string{"vex.imdomestic.com/managed": "true", "vex.imdomestic.com/project": c.cfg.ID}
	ns, err := c.api.CoreV1().Namespaces().Get(ctx, c.ns, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		ns, err = c.api.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: c.ns, Labels: labels}}, metav1.CreateOptions{})
	}
	if err != nil {
		return err
	}
	if !c.projectOwned(ns.Labels) || ns.DeletionTimestamp != nil {
		return fmt.Errorf("project namespace ownership or lifecycle conflicts")
	}
	if err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := c.api.CoreV1().ResourceQuotas(c.ns).Get(ctx, quota.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = c.api.CoreV1().ResourceQuotas(c.ns).Create(ctx, quota, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		if !c.projectOwned(current.Labels) {
			return fmt.Errorf("ResourceQuota ownership mismatch")
		}
		if equality.Semantic.DeepEqual(current.Spec, quota.Spec) {
			return nil
		}
		current.Spec = quota.Spec
		_, err = c.api.CoreV1().ResourceQuotas(c.ns).Update(ctx, current, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return err
	}
	if err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := c.api.CoreV1().LimitRanges(c.ns).Get(ctx, limits.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = c.api.CoreV1().LimitRanges(c.ns).Create(ctx, limits, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		if !c.projectOwned(current.Labels) {
			return fmt.Errorf("LimitRange ownership mismatch")
		}
		if equality.Semantic.DeepEqual(current.Spec, limits.Spec) {
			return nil
		}
		current.Spec = limits.Spec
		_, err = c.api.CoreV1().LimitRanges(c.ns).Update(ctx, current, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := c.api.NetworkingV1().NetworkPolicies(c.ns).Get(ctx, policy.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = c.api.NetworkingV1().NetworkPolicies(c.ns).Create(ctx, policy, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		if !c.projectOwned(current.Labels) {
			return fmt.Errorf("NetworkPolicy ownership mismatch")
		}
		if equality.Semantic.DeepEqual(current.Spec, policy.Spec) {
			return nil
		}
		current.Spec = policy.Spec
		_, err = c.api.NetworkingV1().NetworkPolicies(c.ns).Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

func (c *Client) projectObjects(ctx context.Context) (*corev1.ResourceQuota, *corev1.LimitRange, *networkingv1.NetworkPolicy, error) {
	limit, err := resource.ParseQuantity(c.cfg.Rooms.MemoryLimit)
	if err != nil || limit.Sign() <= 0 || c.cfg.Quota.MaxRooms < 1 {
		return nil, nil, nil, fmt.Errorf("project requires positive memory and room limits")
	}
	total := limit.DeepCopy()
	if !total.Mul(int64(c.cfg.Quota.MaxRooms)) {
		return nil, nil, nil, fmt.Errorf("project memory quota exceeds supported range")
	}
	metadata := metav1.ObjectMeta{Name: "vex-project", Namespace: c.ns, Labels: map[string]string{"vex.imdomestic.com/managed": "true", "vex.imdomestic.com/project": c.cfg.ID}}
	quota := &corev1.ResourceQuota{ObjectMeta: metadata, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
		corev1.ResourcePods:         *resource.NewQuantity(int64(c.cfg.Quota.MaxRooms), resource.DecimalSI),
		corev1.ResourceLimitsMemory: total, corev1.ResourceRequestsMemory: total,
	}}}
	limits := &corev1.LimitRange{ObjectMeta: metadata, Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
		Type: corev1.LimitTypeContainer, Max: corev1.ResourceList{corev1.ResourceMemory: limit},
		Default: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")}, DefaultRequest: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
	}}}}
	endpoint, err := url.Parse(c.cfg.Server.InternalBaseURL)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, nil, nil, fmt.Errorf("invalid internal API endpoint")
	}
	port := 80
	if endpoint.Scheme == "https" {
		port = 443
	}
	if endpoint.Port() != "" {
		port, err = strconv.Atoi(endpoint.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, nil, nil, fmt.Errorf("invalid internal API port")
		}
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", endpoint.Hostname())
	if err != nil {
		return nil, nil, nil, err
	}
	if len(addresses) == 0 {
		return nil, nil, nil, fmt.Errorf("internal API has no addresses")
	}
	apiPeers := make([]networkingv1.NetworkPolicyPeer, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsGlobalUnicast() || address.IsLoopback() {
			return nil, nil, nil, fmt.Errorf("internal API address cannot be reached from project Pods")
		}
		apiPeers = append(apiPeers, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: netip.PrefixFrom(address, address.BitLen()).String()}})
	}
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	gamePort, dnsPort, apiPort := intstr.FromInt32(25565), intstr.FromInt32(53), intstr.FromInt32(int32(port))
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metadata, Spec: networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": c.cfg.Runtime.Kubernetes.SystemNamespace}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "mc-router", "app.kubernetes.io/part-of": "vex"}},
		}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &gamePort}}}},
		Egress: []networkingv1.NetworkPolicyEgressRule{
			{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dnsPort}, {Protocol: &tcp, Port: &dnsPort}}},
			{To: apiPeers, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &apiPort}}},
			{To: []networkingv1.NetworkPolicyPeer{
				{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4"}}},
				{IPBlock: &networkingv1.IPBlock{CIDR: "::/0", Except: []string{"::/80", "64:ff9b::/96", "64:ff9b:1::/48", "fc00::/7", "fe80::/10", "ff00::/8"}}},
			}},
		},
	}}
	return quota, limits, policy, nil
}

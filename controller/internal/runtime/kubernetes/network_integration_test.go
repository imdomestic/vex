//go:build integration

package kubernetes_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/slp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

func (h *lifecycleHarness) execute(id string, arguments ...string) (string, error) {
	h.t.Helper()
	ref := h.ref(id)
	rest, err := clientcmd.BuildConfigFromFlags("", h.cfg.Runtime.Kubernetes.KubeconfigFile)
	if err != nil {
		h.t.Fatal(err)
	}
	request := h.cluster.CoreV1().RESTClient().Post().Resource("pods").Namespace(h.ns).Name(ref.Attributes["objectName"]+"-0").SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "mc", Command: arguments, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(rest, "POST", request.URL())
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(h.ctx, 15*time.Second)
	defer cancel()
	var output bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &output, Stderr: &output})
	return output.String(), err
}

func TestRealProjectRoutesAndNetworkIsolation(t *testing.T) {
	publicURL := os.Getenv("VEX_IT_PUBLIC_HTTPS")
	if os.Getenv("VEX_IT_TWO_ROOMS") != "1" {
		t.Skip("requires an explicitly scheduled two-room integration window")
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" {
		t.Fatal("public egress verification requires a real HTTPS artifact URL without credentials")
	}
	a, b := newLifecycleHarness(t, "routea", false), newLifecycleHarness(t, "routeb", false)
	first, second := a.startCommit(), b.startCommit()
	for _, pair := range []struct {
		h  *lifecycleHarness
		id string
	}{{a, first.ID}, {b, second.ID}} {
		room := pair.h.current(pair.id)
		response, err := slp.Probe(pair.h.ctx, pair.h.cfg.Server.RouterAddress, room.Host, room.Port)
		if err != nil || !slp.Matches(response, room) {
			t.Fatal("actual mc-router did not discover both isolated project Services", err)
		}
		if !strings.Contains(room.Host, "--"+pair.h.cfg.ID+".") {
			t.Fatal("route omitted project identity")
		}
		if _, err := pair.h.execute(room.ID, "getent", "ahostsv4", "kubernetes.default.svc.cluster.local"); err != nil {
			t.Fatal("actual Pod DNS lookup failed", err)
		}
		for _, endpoint := range []string{pair.h.baseURL + "/v2/health", publicURL} {
			if _, err := pair.h.execute(room.ID, "curl", "--silent", "--show-error", "--fail", "--max-time", "10", "--output", "/dev/null", endpoint); err != nil {
				t.Fatal("actual permitted Pod API or public HTTPS request failed", err)
			}
		}
	}
	secondPod, err := b.cluster.CoreV1().Pods(b.ns).Get(b.ctx, b.ref(second.ID).Attributes["objectName"]+"-0", metav1.GetOptions{})
	if err != nil || secondPod.Status.PodIP == "" {
		t.Fatal("actual second project Pod has no IP", err)
	}
	otherAPI, err := url.Parse(b.baseURL)
	if err != nil {
		t.Fatal(err)
	}
	lan, tailnet := os.Getenv("VEX_IT_LAN_SSH"), os.Getenv("VEX_IT_TAILNET_SSH")
	if lan == "" || tailnet == "" {
		t.Fatal("real reachable LAN and tailnet SSH endpoints are required")
	}
	for _, address := range []string{lan, tailnet, otherAPI.Host} {
		connection, err := net.DialTimeout("tcp", address, 5*time.Second)
		if err != nil {
			t.Fatal("denial target must be actually reachable from the test host", err)
		}
		if err = connection.Close(); err != nil {
			t.Fatal(err)
		}
	}
	ownAPI, err := url.Parse(a.baseURL)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(ownAPI.Host)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.execute(first.ID, "timeout", "5", "/bin/bash", "-c", `exec 3<>/dev/tcp/"$1"/"$2"`, "vex-connect", host, port); err != nil {
		t.Fatal("actual TCP probe must succeed against the permitted API before testing denial", err)
	}
	for _, address := range []string{lan, tailnet, otherAPI.Host, net.JoinHostPort(secondPod.Status.PodIP, "25565")} {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.execute(first.ID, "timeout", "5", "/bin/bash", "-c", `exec 3<>/dev/tcp/"$1"/"$2"`, "vex-connect", host, port)
		if err == nil {
			t.Fatalf("project Pod reached forbidden endpoint %s", address)
		}
		var exitError utilexec.ExitError
		if !errors.As(err, &exitError) || (exitError.ExitStatus() != 1 && exitError.ExitStatus() != 124) {
			t.Fatalf("actual TCP probe failed outside the connection attempt: %v", err)
		}
	}
	a.stop(first.ID)
	b.stop(second.ID)
	for _, h := range []*lifecycleHarness{a, b} {
		if err := h.manager.Archive.Step(h.ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("mc-router discovered both actual project rooms; DNS/API/public egress passed; reachable LAN/tailnet/other API and cross-project game connections denied: %s %s", a.ns, b.ns)
}

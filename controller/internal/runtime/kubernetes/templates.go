package kubernetes

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/imdomestic/vex/controller/internal/model"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var jarNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*\.jar$`)

func roomLabels(spec RoomSpec) map[string]string {
	return map[string]string{
		"vex.imdomestic.com/managed": "true", "vex.imdomestic.com/project": spec.Room.Project, "vex.imdomestic.com/room": spec.Room.ID,
		"vex.imdomestic.com/kind": spec.Room.Kind, "vex.imdomestic.com/build": spec.Room.BuildID,
		"vex.imdomestic.com/generation": strconv.FormatInt(spec.Room.Generation, 10),
	}
}

func validDownload(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

func (c *Client) objects(spec RoomSpec) (*corev1.Service, *corev1.Secret, *appsv1.StatefulSet, error) {
	name, err := objectName(spec.Room.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	if !c.cfg.Minecraft.AcceptEULA {
		return nil, nil, nil, fmt.Errorf("Minecraft EULA acceptance is required")
	}
	if spec.Room.Project != c.cfg.ID || spec.Build.Project != c.cfg.ID {
		return nil, nil, nil, fmt.Errorf("room and build must belong to this project")
	}
	if _, err = c.Reference(spec.Room); err != nil {
		return nil, nil, nil, err
	}
	if spec.Room.Generation < 1 || spec.Room.Loader != spec.Build.Loader || spec.Room.BuildID != spec.Build.ID || spec.Build.Status != "ready" || spec.Build.Minecraft == nil || spec.Build.LoaderVersion == nil {
		return nil, nil, nil, fmt.Errorf("room requires a matching ready build and a positive generation")
	}
	serverType, loaderVariable := "", ""
	switch spec.Room.Loader {
	case "fabric":
		serverType, loaderVariable = "FABRIC", "FABRIC_LOADER_VERSION"
	case "neoforge":
		serverType, loaderVariable = "NEOFORGE", "NEOFORGE_VERSION"
	default:
		return nil, nil, nil, fmt.Errorf("unsupported Minecraft loader")
	}
	if spec.Room.Kind != "branch" && spec.Room.Kind != "commit" && spec.Room.Kind != "world" {
		return nil, nil, nil, fmt.Errorf("invalid room kind")
	}
	if !c.nodeAllows(spec.Node, spec.Room.Kind) {
		return nil, nil, nil, fmt.Errorf("room node does not allow this room role")
	}
	if *spec.Build.Minecraft != c.cfg.Minecraft.Version || spec.Build.Java == nil || *spec.Build.Java != c.cfg.Minecraft.Java {
		return nil, nil, nil, fmt.Errorf("build Minecraft or Java runtime differs from project")
	}
	if spec.RCONPassword == "" || spec.RoomToken == "" {
		return nil, nil, nil, fmt.Errorf("room credentials are required")
	}
	if spec.SeedURL != "" && (!validDownload(spec.SeedURL) || !sha256Pattern.MatchString(spec.SeedSHA256)) {
		return nil, nil, nil, fmt.Errorf("seed requires a valid download URL and SHA-256")
	}
	for _, mod := range spec.Mods {
		if !validDownload(mod) {
			return nil, nil, nil, fmt.Errorf("invalid mod download URL")
		}
	}
	downloads := []model.BuildFile{}
	modNames := map[string]bool{}
	for _, file := range spec.Build.Files {
		if file.Side != "both" && file.Side != "server" {
			continue
		}
		if len(downloads) >= len(spec.Mods) || !jarNamePattern.MatchString(file.Name) || modNames[file.Name] || !sha256Pattern.MatchString(file.SHA256) || file.Size <= 0 {
			return nil, nil, nil, fmt.Errorf("invalid server mod manifest")
		}
		file.URL = spec.Mods[len(downloads)]
		modNames[file.Name] = true
		downloads = append(downloads, file)
	}
	if len(downloads) != len(spec.Mods) {
		return nil, nil, nil, fmt.Errorf("server mod manifest and downloads differ")
	}
	modJSON, err := json.Marshal(downloads)
	if err != nil {
		return nil, nil, nil, err
	}
	if spec.ResourcePackURL != "" && (!validDownload(spec.ResourcePackURL) || len(spec.ResourcePackSHA1) != 40) {
		return nil, nil, nil, fmt.Errorf("resource pack requires URL and SHA-1")
	}
	heap := strings.TrimSuffix(strings.TrimSuffix(c.cfg.Rooms.Heap, "G"), "M")
	if strings.HasSuffix(c.cfg.Rooms.Heap, "G") {
		heap += "Gi"
	} else if strings.HasSuffix(c.cfg.Rooms.Heap, "M") {
		heap += "Mi"
	} else {
		heap = c.cfg.Rooms.Heap
	}
	request, err := resource.ParseQuantity(heap)
	if err != nil {
		return nil, nil, nil, err
	}
	limit, err := resource.ParseQuantity(c.cfg.Rooms.MemoryLimit)
	if err != nil {
		return nil, nil, nil, err
	}
	if request.Sign() <= 0 || limit.Cmp(request) < 0 {
		return nil, nil, nil, fmt.Errorf("room memory limit must cover its heap")
	}
	labels := roomLabels(spec)
	selector := map[string]string{"vex.imdomestic.com/managed": "true", "vex.imdomestic.com/project": c.cfg.ID, "vex.imdomestic.com/room": spec.Room.ID, "app.kubernetes.io/component": "minecraft"}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.ns, Labels: labels, Annotations: map[string]string{
			"mc-router.itzg.me/externalServerName": spec.Room.ID + "--" + c.cfg.ID + "." + c.cfg.Server.Zone,
			"mc-router.itzg.me/autoScaleUp":        "false", "mc-router.itzg.me/autoScaleDown": "false",
		}},
		Spec: corev1.ServiceSpec{Selector: selector, Ports: []corev1.ServicePort{{Name: "minecraft", Protocol: corev1.ProtocolTCP, Port: 25565, TargetPort: intstr.FromString("minecraft")}}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-credentials", Namespace: c.ns, Labels: labels},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"rcon-password": []byte(spec.RCONPassword), "room-token": []byte(spec.RoomToken)},
	}
	env := []corev1.EnvVar{
		{Name: "EULA", Value: "TRUE"}, {Name: "TYPE", Value: serverType},
		{Name: "VERSION", Value: *spec.Build.Minecraft}, {Name: loaderVariable, Value: *spec.Build.LoaderVersion},
		{Name: "FABRIC_LAUNCHER_VERSION", Value: "1.1.2"}, {Name: "UID", Value: "1000"}, {Name: "GID", Value: "1000"},
		{Name: "MODS", Value: "/vex-mods"}, {Name: "REMOVE_OLD_MODS", Value: "TRUE"},
		{Name: "ONLINE_MODE", Value: "TRUE"}, {Name: "ENABLE_WHITELIST", Value: "TRUE"}, {Name: "ENFORCE_WHITELIST", Value: "TRUE"},
		{Name: "WHITELIST", Value: strings.Join(c.cfg.Minecraft.Whitelist, ",")},
		{Name: "MEMORY", Value: c.cfg.Rooms.Heap}, {Name: "ACCEPTS_TRANSFERS", Value: "TRUE"},
		{Name: "ENABLE_RCON", Value: "TRUE"}, secretEnv("RCON_PASSWORD", secret.Name, "rcon-password"),
		{Name: "MOTD", Value: fmt.Sprintf("§7vex §f%s\n§8project=%s room=%s build=%s generation=%d", spec.Room.ID, c.cfg.ID, spec.Room.ID, spec.Room.BuildID, spec.Room.Generation)},
		{Name: "LEVEL", Value: "world"}, {Name: "STOP_DURATION", Value: "60"},
		{Name: "TMPDIR", Value: "/data/.work"}, {Name: "JAVA_TOOL_OPTIONS", Value: "-Djava.io.tmpdir=/data/.work -XX:ActiveProcessorCount=2"},
		{Name: "CONSOLE_IN_NAMED_PIPE", Value: "/data/.work/minecraft-console-in"},
		{Name: "VEX_PROJECT", Value: c.cfg.ID}, {Name: "VEX_ROOM_ID", Value: spec.Room.ID}, {Name: "VEX_BUILD_ID", Value: spec.Room.BuildID},
		{Name: "VEX_GENERATION", Value: strconv.FormatInt(spec.Room.Generation, 10)},
		{Name: "VEX_CONTROLLER_URL", Value: c.cfg.Server.InternalBaseURL}, secretEnv("VEX_ROOM_TOKEN", secret.Name, "room-token"),
	}
	if spec.ResourcePackURL != "" {
		env = append(env, corev1.EnvVar{Name: "RESOURCE_PACK", Value: spec.ResourcePackURL}, corev1.EnvVar{Name: "RESOURCE_PACK_SHA1", Value: spec.ResourcePackSHA1})
	}
	podLabels := roomLabels(spec)
	podLabels["app.kubernetes.io/component"] = "minecraft"
	replicas, grace, automount := int32(1), int64(120), false
	storageClass := c.cfg.Runtime.Kubernetes.StorageClass
	workload := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.ns, Labels: labels},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: name, Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: selector},
			PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType, WhenScaled: appsv1.RetainPersistentVolumeClaimRetentionPolicyType},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: podLabels}, Spec: corev1.PodSpec{
				AutomountServiceAccountToken: &automount, TerminationGracePeriodSeconds: &grace,
				NodeSelector: map[string]string{"kubernetes.io/hostname": spec.Node},
				Tolerations:  roomTolerations(),
				Volumes:      []corev1.Volume{{Name: "server-mods", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: quantityPointer("1Gi")}}}},
				InitContainers: []corev1.Container{{Name: "seed-world", Image: c.cfg.Minecraft.Image,
					Command:      []string{"/bin/bash", "-euo", "pipefail", "-c", seedScript},
					Env:          []corev1.EnvVar{{Name: "SEED_URL", Value: spec.SeedURL}, {Name: "SEED_SHA256", Value: spec.SeedSHA256}, {Name: "TMPDIR", Value: "/data/.work"}, {Name: "SERVER_MODS", Value: string(modJSON)}},
					VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}, {Name: "server-mods", MountPath: "/vex-mods"}},
					Resources:    corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")}},
				}},
				Containers: []corev1.Container{{Name: "mc", Image: c.cfg.Minecraft.Image, Env: env,
					Ports:          []corev1.ContainerPort{{Name: "minecraft", ContainerPort: 25565}},
					Resources:      corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: request}, Limits: corev1.ResourceList{corev1.ResourceMemory: limit}},
					StartupProbe:   &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"mc-health"}}}, PeriodSeconds: 5, FailureThreshold: 60},
					ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"mc-health"}}}, PeriodSeconds: 10},
					VolumeMounts:   []corev1.VolumeMount{{Name: "data", MountPath: "/data"}, {Name: "server-mods", MountPath: "/vex-mods", ReadOnly: true}},
				}},
			}},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data", Labels: map[string]string{"vex.imdomestic.com/managed": "true", "vex.imdomestic.com/project": c.cfg.ID, "vex.imdomestic.com/room": spec.Room.ID, "vex.imdomestic.com/kind": spec.Room.Kind}}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &storageClass, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}}}}},
		},
	}
	return service, secret, workload, nil
}

func secretEnv(name, secret, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: key}}}
}

func quantityPointer(value string) *resource.Quantity {
	result := resource.MustParse(value)
	return &result
}

const seedScript = `mkdir -p /data/.work
count=$(jq 'length' <<< "$SERVER_MODS")
for ((index=0; index<count; index++)); do
  item=$(jq -c ".[${index}]" <<< "$SERVER_MODS")
  name=$(jq -r '.name' <<< "$item")
  checksum=$(jq -r '.sha256' <<< "$item")
  size=$(jq -r '.size' <<< "$item")
  download=$(jq -r '.url' <<< "$item")
  curl --fail --silent --show-error --location "$download" -o "/vex-mods/$name.partial"
  printf '%s  %s\n' "$checksum" "/vex-mods/$name.partial" | sha256sum --check --status
  test "$(stat -c %s "/vex-mods/$name.partial")" = "$size"
  mv "/vex-mods/$name.partial" "/vex-mods/$name"
done
chown -R 1000:1000 /vex-mods
if test -e /data/world; then
  test -f /data/world/level.dat
elif test -n "$SEED_URL"; then
  curl --fail --silent --show-error --location "$SEED_URL" -o /data/.work/seed.tar.gz
  printf '%s  %s\n' "$SEED_SHA256" /data/.work/seed.tar.gz | sha256sum --check --status
  tar --extract --gzip --file=/data/.work/seed.tar.gz --directory=/data --no-same-owner --no-same-permissions
  test -f /data/world/level.dat
  (cd /data; find world -type f \( -path world/level.dat -o -path '*/region/*.mca' \) -print0) |
    sort -z | while IFS= read -r -d '' member; do
      checksum=$(sha256sum "/data/$member")
      checksum=${checksum%% *}
      jq -cn --arg path "$member" --arg sha256 "$checksum" '{event:"seed-file-sha256",path:$path,sha256:$sha256}'
    done
fi
chown -R 1000:1000 /data
`

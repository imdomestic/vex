package config

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

func fieldError(path, reason string) error {
	return fmt.Errorf("%s: %s", path, reason)
}

func positiveDuration(path, value string) error {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return fieldError(path, "requires a positive duration")
	}
	return nil
}

func validURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func validAddress(value string) bool {
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	number, err := strconv.Atoi(port)
	return err == nil && number > 0 && number <= 65535
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}

func (c Config) Validate() error {
	if !filepath.IsAbs(c.Server.DataDir) {
		return fieldError("server.data_dir", "requires an absolute path")
	}
	if err := c.Server.validateListeners(); err != nil {
		return err
	}
	if !validURL(c.Server.PublicBaseURL) || !validURL(c.Server.InternalBaseURL) {
		return fieldError("server", "requires HTTP base URLs without credentials, query or fragment")
	}
	if c.Server.Zone == "" || strings.ContainsAny(c.Server.Zone, "/: \t\r\n") || !validAddress(c.Server.RouterAddress) {
		return fieldError("server", "requires zone and router_address")
	}
	if c.Runtime.Backend != "kubernetes" {
		return fieldError("runtime.backend", "P1 requires kubernetes")
	}
	kubernetes := c.Runtime.Kubernetes
	if !filepath.IsAbs(kubernetes.KubeconfigFile) || kubernetes.StorageClass == "" {
		return fieldError("runtime.kubernetes", "requires absolute kubeconfig_file and storage_class")
	}
	prefix := kubernetes.NamespacePrefix
	if !strings.HasSuffix(prefix, "-") || !validDNSLabel(strings.TrimSuffix(prefix, "-")) || len(prefix)+23 > 63 {
		return fieldError("runtime.kubernetes.namespace_prefix", "requires a DNS label followed by '-' and at most 40 bytes")
	}
	if !validDNSLabel(kubernetes.SystemNamespace) {
		return fieldError("runtime.kubernetes.system_namespace", "requires a DNS label of at most 63 bytes")
	}
	for project := range c.Projects {
		if kubernetes.SystemNamespace == prefix+project {
			return fieldError("runtime.kubernetes.system_namespace", "must differ from every project namespace")
		}
	}
	for id, node := range kubernetes.Nodes {
		if !ValidID(id) || node.K8sName == "" || len(node.Roles) == 0 || node.MaxRooms < 0 {
			return fieldError("runtime.kubernetes.nodes", "invalid node ID, name, roles or capacity")
		}
		for _, role := range node.Roles {
			if role != "persistent" && role != "ephemeral" && role != "build" {
				return fieldError("runtime.kubernetes.nodes."+id+".roles", "unknown role")
			}
		}
	}
	for _, source := range kubernetes.WebhookSources {
		_, _, err := net.ParseCIDR(source)
		if err != nil && net.ParseIP(source) == nil {
			return fieldError("runtime.kubernetes.webhook_sources", "requires IP addresses or CIDRs")
		}
	}
	switch c.Archive.Backend {
	case "local":
		if !filepath.IsAbs(c.Archive.Local.Directory) {
			return fieldError("archive.local.directory", "requires an absolute path")
		}
	case "rsync-ssh":
		if c.Archive.RsyncSSH.Target == "" || !filepath.IsAbs(c.Archive.RsyncSSH.IdentityFile) || !filepath.IsAbs(c.Archive.RsyncSSH.KnownHostsFile) {
			return fieldError("archive.rsync_ssh", "requires target, absolute identity_file and known_hosts_file")
		}
	default:
		return fieldError("archive.backend", "requires local or rsync-ssh")
	}
	if !filepath.IsAbs(c.Auth.SigningKeyFile) {
		return fieldError("auth.signing_key_file", "requires an absolute path")
	}
	if strings.TrimSpace(c.Server.Name) == "" || strings.ContainsAny(c.Server.Name, "\x00\r\n") {
		return fieldError("server.name", "requires a nonempty server name")
	}
	if !filepath.IsAbs(c.Auth.AdminSocket) || len(c.Auth.AdminSocket) > 100 {
		return fieldError("auth.admin_socket", "requires an absolute Unix socket path of at most 100 bytes")
	}
	for path, value := range map[string]string{
		"auth.session_ttl": c.Auth.SessionTTL, "auth.invite_ttl": c.Auth.InviteTTL, "rooms.idle.commit": c.Rooms.Idle.Commit,
		"rooms.idle.branch": c.Rooms.Idle.Branch, "rooms.idle.world": c.Rooms.Idle.World,
		"rooms.join_lease": c.Rooms.JoinLease, "rooms.start_timeout": c.Rooms.StartTimeout,
	} {
		if err := positiveDuration(path, value); err != nil {
			return err
		}
	}
	if c.Build.MaxConcurrent != 1 || !filepath.IsAbs(c.Build.LockFile) {
		return fieldError("build", "requires max_concurrent=1 and absolute lock_file")
	}
	if c.Rooms.Heap == "" {
		return fieldError("rooms.heap", "is required")
	}
	if c.Rooms.DefaultSeed != "empty" {
		kind, id, ok := strings.Cut(c.Rooms.DefaultSeed, ":")
		if !ok || strings.TrimSpace(id) == "" || (kind != "branch" && kind != "snapshot" && kind != "world") {
			return fieldError("rooms.default_seed", "requires empty, branch:<name>, snapshot:<id> or world:<id>")
		}
	}
	if c.Worlds.ScannerFile != "" && !filepath.IsAbs(c.Worlds.ScannerFile) {
		return fieldError("worlds.scanner_file", "requires an absolute path")
	}
	identities := map[string]string{}
	digests := map[string]bool{}
	for id, user := range c.Users {
		if !ValidID(id) {
			return fieldError("users", "invalid user ID")
		}
		if user.TokenSHA256 != "" {
			if !validDigest(user.TokenSHA256) || digests[user.TokenSHA256] {
				return fieldError("users."+id+".token_sha256", "requires a unique lowercase SHA256 digest")
			}
			digests[user.TokenSHA256] = true
		}
		for _, identity := range append(append([]string{}, user.Identities...), "static:"+id) {
			provider, subject, ok := strings.Cut(identity, ":")
			if !ok || !ValidID(provider) || subject == "" || strings.ContainsAny(subject, "\x00\r\n") {
				return fieldError("users."+id+".identities", "requires provider:subject values")
			}
			if owner, exists := identities[identity]; exists && owner != id {
				return fieldError("users."+id+".identities", "identity belongs to multiple users")
			}
			identities[identity] = id
		}
		for _, player := range user.Minecraft {
			if _, err := uuid.Parse(player); err != nil {
				return fieldError("users."+id+".minecraft", "requires Minecraft UUIDs")
			}
		}
	}
	for id, runtime := range c.Runtimes {
		if !ValidID(id) || runtime.ServerImage == "" || !filepath.IsAbs(runtime.JDKHome) {
			return fieldError("runtimes", "requires valid IDs, server_image and absolute jdk_home")
		}
	}
	for id, agent := range c.Agents {
		if !ValidID(id) || agent.Minecraft == "" || !ValidID(agent.Loader) || agent.Version == "" || !filepath.IsAbs(agent.File) {
			return fieldError("agents", "requires valid IDs, minecraft, loader, version and absolute file")
		}
	}
	for id, project := range c.Projects {
		if err := c.validateProject(id, project); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) validateProject(id string, p Project) error {
	path := "projects." + id
	if !ValidID(id) {
		return fieldError("projects", "invalid project ID")
	}
	if p.Remote == "" || p.DefaultBranch == "" {
		return fieldError(path, "requires remote and default_branch")
	}
	if strings.HasPrefix(p.Remote, "http://") || strings.HasPrefix(p.Remote, "https://") {
		remote, err := url.Parse(p.Remote)
		if err != nil || remote.User != nil || remote.RawQuery != "" || remote.Fragment != "" {
			return fieldError(path+".remote", "HTTP remote cannot contain credentials, query or fragment")
		}
	}
	if p.Trust != "trusted" && p.Trust != "untrusted" {
		return fieldError(path+".trust", "requires trusted or untrusted")
	}
	if p.Build.Source != "host" {
		return fieldError(path+".build.source", "P1 requires host")
	}
	if p.Minecraft.Version == "" || p.Minecraft.Java < 1 {
		return fieldError(path+".minecraft", "requires version and a positive Java version")
	}
	if _, ok := c.Runtimes[fmt.Sprintf("java%d", p.Minecraft.Java)]; !ok {
		return fieldError(path+".minecraft.java", "runtime is not configured")
	}
	if err := positiveDuration(path+".git.poll_interval", p.Git.PollInterval); err != nil {
		return err
	}
	if err := positiveDuration(path+".build.timeout", p.Build.Timeout); err != nil {
		return err
	}
	if (p.Git.SSHIdentityFile == "") != (p.Git.KnownHostsFile == "") || (p.Git.SSHIdentityFile != "" && p.Git.HTTPSTokenFile != "") {
		return fieldError(path+".git", "SSH requires both files and cannot combine HTTPS credentials")
	}
	for field, value := range map[string]string{"ssh_identity_file": p.Git.SSHIdentityFile, "known_hosts_file": p.Git.KnownHostsFile, "https_token_file": p.Git.HTTPSTokenFile} {
		if value != "" && !filepath.IsAbs(value) {
			return fieldError(path+".git."+field, "requires an absolute path")
		}
	}
	if p.Quota.MaxRooms < 1 || p.Quota.RoomMemoryMax == "" || p.Quota.BuildCache < 1 || p.Quota.SnapshotKeep < 0 || p.Quota.SnapshotKeepDays < 0 {
		return fieldError(path+".quota", "invalid room, build or snapshot quota")
	}
	if len(p.EnabledLoaders()) == 0 {
		return fieldError(path+".loaders", "requires an enabled loader")
	}
	for id, loader := range p.Loaders {
		if !ValidID(id) {
			return fieldError(path+".loaders", "invalid loader ID")
		}
		if id != "fabric" && id != "neoforge" {
			return fieldError(path+".loaders."+id, "P1 supports fabric and neoforge")
		}
		if loader.Enabled && (loader.GradleTask == "" || loader.ArtifactGlob == "" || loader.LoaderVersionProperty == "") {
			return fieldError(path+".loaders."+id, "requires gradle_task, artifact_glob and loader_version_property")
		}
		if loader.Agent != "" {
			agent, ok := c.Agents[loader.Agent]
			if !ok || agent.Minecraft != p.Minecraft.Version || agent.Loader != id {
				return fieldError(path+".loaders."+id+".agent", "requires an agent for the same Minecraft and loader")
			}
		}
		for _, dep := range loader.Deps {
			if dep.Name == "" || dep.URL == "" || (dep.Side != "both" && dep.Side != "client" && dep.Side != "server") || (dep.SHA256 != "" && !validDigest(dep.SHA256)) {
				return fieldError(path+".loaders."+id+".deps", "invalid dependency")
			}
		}
	}
	for user, role := range p.Members {
		if _, ok := c.Users[user]; !ok || !role.Allows(Viewer) {
			return fieldError(path+".members", "requires configured users and owner, developer or viewer roles")
		}
	}
	for _, entry := range p.Worlds.ExtraRootEntries {
		if entry == "" || entry == "." || entry == ".." || strings.ContainsAny(entry, "/\\\x00") {
			return fieldError(path+".worlds.extra_root_entries", "requires single root entry names")
		}
	}
	return nil
}

func (c Client) Validate() error {
	for name, server := range c.Servers {
		if !ValidID(name) || !validURL(server.URL) || server.Project != "" && !ValidID(server.Project) {
			return fieldError("servers", "requires valid IDs, HTTP URLs and project IDs")
		}
	}
	if c.Server != "" && !ValidID(c.Server) || c.DefaultServer != "" && !ValidID(c.DefaultServer) {
		return fieldError("server", "requires a valid server ID")
	}
	if c.DefaultServer != "" {
		if _, ok := c.Servers[c.DefaultServer]; !ok {
			return fieldError("default_server", "does not name a configured server")
		}
	}
	if !validURL(c.URL) {
		return fieldError("url", "requires an HTTP URL without credentials, query or fragment")
	}
	if c.Project != "" && !ValidID(c.Project) {
		return fieldError("project", "invalid project ID")
	}
	if c.TokenFile != "" && !filepath.IsAbs(c.TokenFile) {
		return fieldError("token_file", "requires an absolute path")
	}
	return positiveDuration("timeout", c.Timeout)
}

var dnsLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

func validDNSLabel(value string) bool {
	return len(value) <= 63 && dnsLabelPattern.MatchString(value)
}

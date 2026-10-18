{ config, lib, pkgs, ... }:
let
  cfg = config.services.vexd;
  format = pkgs.formats.toml { };
  settingsFile = format.generate "vexd.toml" cfg.settings;
  dataDir = cfg.settings.server.data_dir or "/var/lib/vex";
  lockFile = cfg.settings.build.lock_file or "/var/lib/vex/build.lock";
  adminSocket = cfg.settings.auth.admin_socket or "/run/vex/admin.sock";
  adminDirectory = builtins.dirOf adminSocket;
  dataOwner = if dataDir == "/var/lib/vex" then "root root" else "vexd vexd";
  executable = if cfg.executable != null then cfg.executable else "${cfg.package}/bin/vexd";
  paths = with pkgs; [ bash coreutils git gnutar gzip openssh rsync systemd util-linux ];
  projectSecrets = lib.concatMap (project:
    lib.filter (value: value != "") [
      (project.git.ssh_identity_file or "")
      (project.git.known_hosts_file or "")
      (project.git.https_token_file or "")
    ]) (builtins.attrValues (cfg.settings.projects or { }));
  secrets = lib.unique (lib.filter (value: value != "") ([
    (cfg.settings.auth.signing_key_file or "/var/lib/vex/secrets/signing.key")
    (cfg.settings.runtime.kubernetes.kubeconfig_file or "/var/lib/vex/kubeconfig")
    (cfg.settings.archive.rsync_ssh.identity_file or "")
    (cfg.settings.archive.rsync_ssh.known_hosts_file or "")
    (cfg.settings.github.app.private_key_file or "")
    (cfg.settings.github.app.client_secret_file or "")
    (cfg.settings.github.app.webhook_secret_file or "")
    (cfg.settings.server.tls_key_file or "")
  ] ++ projectSecrets));
  common = {
    UMask = "0077";
    NoNewPrivileges = true;
    ProtectSystem = "strict";
    ProtectHome = true;
    PrivateTmp = false;
    PrivateDevices = true;
    ProtectKernelTunables = true;
    ProtectKernelModules = true;
    ProtectControlGroups = true;
    RestrictSUIDSGID = true;
    RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
    KillMode = "control-group";
  };
  dnsConfig = pkgs.writeText "vex-dnsmasq.conf" ''
    no-resolv
    no-hosts
    bind-dynamic
    listen-address=${lib.concatStringsSep "," cfg.dns.listenAddresses}
    address=/${cfg.settings.server.zone or "vex.internal"}/${cfg.dns.answerAddress}
    local=/${cfg.settings.server.zone or "vex.internal"}/
    pid-file=/run/vex-dns/dnsmasq.pid
  '';
in
{
  options.services.vexd = {
    enable = lib.mkEnableOption "Vex Minecraft test deployment controller";
    package = lib.mkOption {
      type = lib.types.nullOr lib.types.package;
      default = null;
      description = "Vex application package.";
    };
    executable = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Absolute vexd executable path, for an independently updated application profile.";
    };
    settings = lib.mkOption {
      type = format.type;
      default = { };
      description = "Vex settings rendered as TOML. Secrets are referenced through file paths.";
    };
    adminUsers = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Local users allowed to manage invitations and sessions through the Unix socket.";
    };
    buildJDKs = lib.mkOption {
      type = lib.types.listOf lib.types.package;
      default = [ pkgs.jdk25 ];
      description = "JDK packages retained for trusted host builds.";
    };
    dns = {
      enable = lib.mkEnableOption "Vex zone DNS responder";
      listenAddresses = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Addresses on which the zone responder listens.";
      };
      answerAddress = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = "Minecraft router address returned for the Vex zone.";
      };
    };
    firewallInterfaces = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Interfaces allowing API, Minecraft and optional zone DNS traffic.";
    };
    apiPort = lib.mkOption {
      type = lib.types.port;
      default = 8790;
      description = "API port opened on firewallInterfaces.";
    };
    minecraftPort = lib.mkOption {
      type = lib.types.port;
      default = 25565;
      description = "Minecraft router port opened on firewallInterfaces.";
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.package != null || cfg.executable != null;
        message = "services.vexd requires package or executable.";
      }
      {
        assertion = lib.hasPrefix "/" dataDir && lib.hasPrefix "/" executable && lib.hasPrefix "/" lockFile;
        message = "Vex executable, data directory and build lock must be absolute paths.";
      }
      {
        assertion = !cfg.dns.enable || (cfg.dns.listenAddresses != [ ] && cfg.dns.answerAddress != "");
        message = "Vex DNS requires listenAddresses and answerAddress.";
      }
    ];
    users.groups.vexd = { };
    users.groups.vex-build = { };
    users.groups.vex-admin.members = cfg.adminUsers;
    users.users.vexd = {
      isSystemUser = true;
      group = "vexd";
      extraGroups = [ "vex-build" "vex-admin" ];
      home = dataDir;
      shell = "${pkgs.shadow}/bin/nologin";
      description = "Vex controller";
    };
    users.users.vex-build = {
      isSystemUser = true;
      group = "vex-build";
      shell = "${pkgs.shadow}/bin/nologin";
      description = "Vex trusted host build worker";
    };
    environment.etc."vex/vexd.toml".source = settingsFile;
    environment.systemPackages = cfg.buildJDKs;
    systemd.tmpfiles.rules = lib.unique [
      "d ${dataDir} 0711 ${dataOwner} -"
      "d ${dataDir}/projects 0711 vexd vexd -"
      "d ${dataDir}/jobs 0750 vexd vex-build -"
      "d ${dataDir}/state 0700 vexd vexd -"
      "d ${dataDir}/.work 0700 vexd vexd -"
      "d /var/lib/vex 0711 root root -"
      "d /var/lib/vex/secrets 0750 root vexd -"
      "d /var/lib/vex/ssh 0700 vexd vexd -"
      "f ${lockFile} 0444 root root -"
      "d ${adminDirectory} 0750 vexd vex-admin -"
    ] ++ lib.optional ((cfg.settings.archive.backend or "local") == "local")
      "d ${cfg.settings.archive.local.directory or "/var/lib/vex/archive"} 0700 vexd vexd -";
    systemd.services.vexd = {
      description = "Vex deployment controller";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      path = paths;
      environment = {
        TMPDIR = "${dataDir}/.work";
        SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
      };
      serviceConfig = common // {
        User = "vexd";
        Group = "vexd";
        SupplementaryGroups = [ "vex-build" "vex-admin" ];
        ReadWritePaths = [ dataDir "/var/lib/vex" adminDirectory ];
        Type = "simple";
        ExecStart = "${executable} serve --config /etc/vex/vexd.toml";
        ExecReload = lib.mkIf ((cfg.settings.server.tls_cert_file or "") != "")
          "${pkgs.coreutils}/bin/kill -HUP $MAINPID";
        WorkingDirectory = dataDir;
        Restart = "on-failure";
        RestartSec = 5;
        TimeoutStopSec = 60;
      };
    };
    systemd.services."vex-build@" = {
      description = "Vex project build %i";
      path = paths ++ cfg.buildJDKs;
      environment.SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
      serviceConfig = common // {
        User = "vex-build";
        Group = "vex-build";
        UMask = "0007";
        Type = "exec";
        ExecStart = "${executable} build-worker --job ${dataDir}/jobs/%i.json";
        WorkingDirectory = dataDir;
        ReadWritePaths = [ "${dataDir}/projects" ];
        InaccessiblePaths = [ "${dataDir}/state" "/var/lib/vex/secrets" "/var/lib/vex/ssh" ] ++ secrets;
        StandardOutput = "journal";
        StandardError = "inherit";
        RemainAfterExit = true;
        MemoryHigh = "5G";
        MemoryMax = "6G";
        CPUQuota = "200%";
        OOMPolicy = "stop";
        TimeoutStopSec = 30;
      };
    };
    security.polkit.extraConfig = ''
      polkit.addRule(function(action, subject) {
        var unit = action.lookup("unit");
        var verb = action.lookup("verb");
        if (subject.user === "vexd" &&
            action.id === "org.freedesktop.systemd1.manage-units" &&
            /^vex-build@[a-z][a-z0-9-]{0,22}--(?:fabric|neoforge)-[0-9a-f]{12}\.service$/.test(unit) &&
            ["start", "stop", "reset-failed"].indexOf(verb) >= 0) {
          return polkit.Result.YES;
        }
      });
    '';
    users.groups.vex-dns = lib.mkIf cfg.dns.enable { };
    users.users.vex-dns = lib.mkIf cfg.dns.enable {
      isSystemUser = true;
      group = "vex-dns";
      shell = "${pkgs.shadow}/bin/nologin";
    };
    systemd.services.vex-dns = lib.mkIf cfg.dns.enable {
      description = "Vex zone DNS";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      environment.TMPDIR = "/var/cache/vex-dns";
      serviceConfig = common // {
        User = "vex-dns";
        Group = "vex-dns";
        Type = "simple";
        ExecStartPre = "${pkgs.dnsmasq}/bin/dnsmasq --test --conf-file=${dnsConfig}";
        ExecStart = "${pkgs.dnsmasq}/bin/dnsmasq --keep-in-foreground --user=vex-dns --conf-file=${dnsConfig}";
        Restart = "on-failure";
        RestartSec = 3;
        AmbientCapabilities = [ "CAP_NET_BIND_SERVICE" ];
        CapabilityBoundingSet = [ "CAP_NET_BIND_SERVICE" ];
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" "AF_NETLINK" ];
        RuntimeDirectory = "vex-dns";
        RuntimeDirectoryMode = "0700";
        CacheDirectory = "vex-dns";
        CacheDirectoryMode = "0700";
      };
    };
    networking.firewall.interfaces = lib.genAttrs cfg.firewallInterfaces (_: {
      allowedTCPPorts = [ cfg.apiPort cfg.minecraftPort ] ++ lib.optional cfg.dns.enable 53;
      allowedUDPPorts = lib.optional cfg.dns.enable 53;
    });
  };
}

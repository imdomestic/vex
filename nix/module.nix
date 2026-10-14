{ config, lib, pkgs, ... }:
let
  cfg = config.services.chorusd;
  worldScanner = import ./world-scanner.nix { inherit pkgs; };
  dnsConfig = pkgs.writeText "chorus-dnsmasq.conf" ''
    no-resolv
    no-hosts
    bind-dynamic
    listen-address=100.64.0.43,10.1.2.137
    address=/chorus.internal/100.64.0.43
    local=/chorus.internal/
    pid-file=/run/chorus-dns/dnsmasq.pid
  '';
  paths = [ pkgs.bash pkgs.coreutils pkgs.git pkgs.gnutar pkgs.gzip pkgs.openssh pkgs.rsync pkgs.systemd pkgs.util-linux ];
  serviceConfig = {
    User = "chorusd";
    Group = "chorusd";
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
    ReadWritePaths = [ cfg.dataDir "/var/lib/chorusd" ];
    KillMode = "control-group";
  };
in
{
  options.services.chorusd = {
    enable = lib.mkEnableOption "Chorus controller";
    package = lib.mkOption {
      type = lib.types.package;
      description = "chorusd executable package";
    };
    configFile = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/chorusd/config.toml";
      description = "Runtime configuration containing secret file paths";
    };
    dataDir = lib.mkOption {
      type = lib.types.str;
      default = "/srv/chorus";
    };
    jdk = lib.mkOption {
      type = lib.types.package;
      default = pkgs.jdk25;
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = config.networking.hostName == "encore";
        message = "Chorus controller configuration requires encore";
      }
      {
        assertion = lib.hasPrefix "/" cfg.configFile && lib.hasPrefix "/" cfg.dataDir;
        message = "Chorus configuration and data paths must be absolute";
      }
    ];

    users.users.chorusd = {
      isSystemUser = true;
      uid = 980;
      group = "chorusd";
      shell = "${pkgs.shadow}/bin/nologin";
      home = cfg.dataDir;
      description = "Chorus room controller";
    };
    users.groups.chorusd.gid = 980;
    users.users.chorus-dns = {
      isSystemUser = true;
      group = "chorus-dns";
      shell = "${pkgs.shadow}/bin/nologin";
      description = "Chorus DNS responder";
    };
    users.groups.chorus-dns = { };

    systemd.tmpfiles.rules = [
      "f /srv/chorus-build.lock 0444 root root -"
      "d ${cfg.dataDir} 0750 chorusd chorusd -"
      "d ${cfg.dataDir}/.work 0700 chorusd chorusd -"
      "d ${cfg.dataDir}/state 0700 chorusd chorusd -"
      "d ${cfg.dataDir}/builds 0700 chorusd chorusd -"
      "d ${cfg.dataDir}/work 0700 chorusd chorusd -"
      "d /var/lib/chorusd 0750 chorusd chorusd -"
      "d /var/lib/chorusd/secrets 0750 root chorusd -"
      "L+ /var/lib/chorusd/world-scanner - root root - ${worldScanner}/bin/chorus-world-scanner"
    ];

    systemd.services.chorusd = {
      description = "Chorus room controller";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" "k3s.service" ];
      wants = [ "network-online.target" ];
      requires = [ "k3s.service" ];
      path = paths;
      environment = {
        TMPDIR = "${cfg.dataDir}/.work";
        JAVA_HOME = cfg.jdk.home;
        SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
      };
      serviceConfig = serviceConfig // {
        Type = "simple";
        ExecStart = "${lib.getExe cfg.package} serve --config ${cfg.configFile}";
        WorkingDirectory = cfg.dataDir;
        Restart = "on-failure";
        RestartSec = 5;
        TimeoutStopSec = 60;
      };
    };

    systemd.services."chorus-build@" = {
      description = "Chorus Fabric build %i";
      path = paths ++ [ cfg.jdk ];
      environment = {
        TMPDIR = "${cfg.dataDir}/work/%i/.work";
        JAVA_HOME = cfg.jdk.home;
        JAVA_TOOL_OPTIONS = "-Djava.io.tmpdir=${cfg.dataDir}/work/%i/.work -XX:ActiveProcessorCount=2";
        GRADLE_USER_HOME = "${cfg.dataDir}/gradle";
        SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
      };
      serviceConfig = serviceConfig // {
        Type = "exec";
        ExecStart = "${lib.getExe cfg.package} build-worker --config ${cfg.configFile} --id %i";
        WorkingDirectory = "${cfg.dataDir}/work/%i";
        StandardOutput = "append:${cfg.dataDir}/builds/%i/build.log";
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
        if (subject.user === "chorusd" &&
            action.id === "org.freedesktop.systemd1.manage-units" &&
            /^chorus-build@fabric-[0-9a-f]{12}\.service$/.test(unit) &&
            ["start", "stop", "reset-failed"].indexOf(verb) >= 0) {
          return polkit.Result.YES;
        }
      });
    '';

    systemd.services.dnsmasq = {
      description = "Chorus internal DNS";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      environment.TMPDIR = "/var/cache/chorus-dns";
      serviceConfig = {
        Type = "simple";
        User = "chorus-dns";
        Group = "chorus-dns";
        ExecStartPre = "${pkgs.dnsmasq}/bin/dnsmasq --test --conf-file=${dnsConfig}";
        ExecStart = "${pkgs.dnsmasq}/bin/dnsmasq --keep-in-foreground --user=chorus-dns --conf-file=${dnsConfig}";
        Restart = "on-failure";
        RestartSec = 3;
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = false;
        PrivateDevices = true;
        AmbientCapabilities = [ "CAP_NET_BIND_SERVICE" ];
        CapabilityBoundingSet = [ "CAP_NET_BIND_SERVICE" ];
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" "AF_NETLINK" ];
        RuntimeDirectory = "chorus-dns";
        RuntimeDirectoryMode = "0700";
        CacheDirectory = "chorus-dns";
        CacheDirectoryMode = "0700";
      };
    };
    networking.firewall.interfaces = lib.genAttrs [ "tailscale0" "wlo1" ] (_: {
      allowedTCPPorts = [ 53 8790 25565 ];
      allowedUDPPorts = [ 53 ];
    });
  };
}

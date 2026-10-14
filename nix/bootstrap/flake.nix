{
  description = "Chorus Testbed encore k3s bootstrap";

  inputs.encoreBase.url = "github:imdomestic/nix-config/cc004e5814bb2dbef08186901b71f65c83bae064";

  outputs = { encoreBase, ... }:
    let
      baseline = encoreBase.nixosConfigurations.encore.extendModules {
        modules = [ ./running-system.nix ];
      };
      expectedBaseline = "/nix/store/9s5vlyqfk6l59lmbn27gd7mlyzyv78mv-nixos-system-encore-26.05.20260911.21a67dc";
      k3sModules = [ ./k3s-server.nix ./metrics-server.nix ];
      encore = assert baseline.config.system.build.toplevel.outPath == expectedBaseline;
        baseline.extendModules {
          modules = k3sModules;
        };
      retiredEncore = encore.extendModules {
        modules = [
          ./retire-legacy-minecraft.nix
          {
            system.extraDependencies = [ encore.config.system.build.toplevel ];
          }
        ];
      };
    in
    {
      nixosConfigurations.encore = retiredEncore;
      nixosConfigurations.encore-retired = retiredEncore;
      nixosModules.encore-k3s = { imports = k3sModules; };
      nixosModules.retire-legacy-minecraft = import ./retire-legacy-minecraft.nix;
      packages.x86_64-linux.default = retiredEncore.config.system.build.toplevel;
      packages.x86_64-linux.retired = retiredEncore.config.system.build.toplevel;
      checks.x86_64-linux.encore = retiredEncore.config.system.build.toplevel;
      checks.x86_64-linux.encore-retired = retiredEncore.config.system.build.toplevel;
      bootstrapReport = {
        baseline = baseline.config.system.build.toplevel.outPath;
        system = retiredEncore.config.system.build.toplevel.outPath;
        retiredSystem = retiredEncore.config.system.build.toplevel.outPath;
        k3sVersion = encore.config.services.k3s.package.version;
        k3sCommand = encore.config.systemd.services.k3s.serviceConfig.ExecStart;
        firewallRules = encore.config.networking.firewall.extraInputRules;
      };
    };
}

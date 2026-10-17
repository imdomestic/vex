{
  description = "Vex Minecraft mod test deployment platform";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/21a67dc470149f337cecafbe965d8d252a390518";
  outputs = { self, nixpkgs, ... }:
    let
      pkgs = nixpkgs.legacyPackages.x86_64-linux;
      controller = pkgs.callPackage ./nix/package.nix {
        vendorHash = pkgs.lib.removeSuffix "\n" (builtins.readFile ./nix/vendor-hash);
      };
      scanner = import ./nix/world-scanner.nix { inherit pkgs; };
      application = pkgs.symlinkJoin {
        name = "vex-applications";
        paths = [ controller scanner ];
      };
    in {
      packages.x86_64-linux = {
        default = application;
        vex = application;
        vexd = controller;
        world-scanner = scanner;
      };
      checks.x86_64-linux.vexd = controller;
      nixosModules.vexd = { lib, pkgs, ... }: {
        imports = [ ./nix/module.nix ];
        services.vexd.package = lib.mkDefault self.packages.${pkgs.system}.vex;
      };
    };
}

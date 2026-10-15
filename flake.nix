{
  description = "Chorus controller and world scanner applications";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/21a67dc470149f337cecafbe965d8d252a390518";
  outputs = { nixpkgs, ... }:
    let
      pkgs = nixpkgs.legacyPackages.x86_64-linux;
      controller = pkgs.callPackage ./nix/package.nix {
        vendorHash = pkgs.lib.removeSuffix "\n" (builtins.readFile ./nix/vendor-hash);
      };
      scanner = import ./nix/world-scanner.nix { inherit pkgs; };
      application = pkgs.symlinkJoin {
        name = "chorus-applications";
        paths = [ controller scanner ];
      };
    in {
      packages.x86_64-linux = {
        default = application;
        chorus = application;
        chorusd = controller;
        world-scanner = scanner;
      };
      checks.x86_64-linux.chorusd = controller;
    };
}

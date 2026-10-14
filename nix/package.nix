{ lib, buildGoModule, git, vendorHash }:
buildGoModule {
  pname = "chorusd";
  version = "0.1.0";
  src = lib.cleanSource ../controller;
  inherit vendorHash;
  subPackages = [ "cmd/chorusd" "cmd/chorusctl" ];
  ldflags = [ "-s" "-w" ];
  nativeCheckInputs = [ git ];
  checkPhase = ''
    runHook preCheck
    go test -p "$NIX_BUILD_CORES" ./...
    runHook postCheck
  '';
  meta = {
    description = "Chorus room and build controller";
    mainProgram = "chorusd";
    platforms = lib.platforms.linux;
  };
}

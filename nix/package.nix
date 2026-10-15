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
    unformatted=$(gofmt -l cmd internal)
    if [ -n "$unformatted" ]; then
      printf '%s\n' "$unformatted" >&2
      exit 1
    fi
    go test -p "$NIX_BUILD_CORES" ./...
    runHook postCheck
  '';
  meta = {
    description = "Chorus room and build controller";
    mainProgram = "chorusd";
    platforms = lib.platforms.linux;
  };
}

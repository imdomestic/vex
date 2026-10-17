{ lib, buildGoModule, git, makeWrapper, libsecret, vendorHash }:
buildGoModule {
  pname = "vexd";
  version = "0.3.0";
  src = lib.cleanSource ../controller;
  inherit vendorHash;
  subPackages = [ "cmd/vexd" "cmd/vex" ];
  ldflags = [ "-s" "-w" ];
  nativeCheckInputs = [ git ];
  nativeBuildInputs = [ makeWrapper ];
  postInstall = ''
    wrapProgram "$out/bin/vex" --prefix PATH : ${lib.makeBinPath [ libsecret ]}
  '';
  VEX_API_DOCUMENT = ../docs/api.md;
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
    description = "Vex project, room and build controller";
    mainProgram = "vexd";
    platforms = lib.platforms.linux;
  };
}

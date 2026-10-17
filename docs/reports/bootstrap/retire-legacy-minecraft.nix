{ lib, ... }:
let
  unitNames = [
    "minecraft-server-GTL"
    "minecraft-server-bedrock-proxy"
    "minecraft-server-bingo"
    "minecraft-server-lobby"
    "minecraft-server-proxy"
    "minecraft-server-speedrun"
  ];
  disableUnit = _: {
    enable = lib.mkForce false;
    wantedBy = lib.mkForce [ ];
  };
in
{
  systemd.services = lib.genAttrs unitNames disableUnit;
  systemd.sockets = lib.genAttrs unitNames disableUnit;
}

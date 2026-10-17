import argparse
import json
from datetime import datetime, timezone

from mcstatus import BedrockServer, JavaServer


def main():
    parser = argparse.ArgumentParser(description="查询现有 Minecraft 服务的在线人数")
    parser.add_argument("--mac-tunnels", action="store_true")
    parser.add_argument("--service", choices=("proxy", "GTL", "speedrun", "lobby", "bingo", "bedrock-proxy-java", "bedrock-proxy-bedrock"))
    args = parser.parse_args()
    ports = {"GTL": 25560, "speedrun": 25567, "lobby": 25568, "bingo": 25573}
    services = [("proxy", "127.0.0.1", 35565)] if args.mac_tunnels else [("proxy", "127.0.0.1", 25565)]
    for name, port in ports.items():
        services.append((name, "127.0.0.1", port + 10000 if args.mac_tunnels else port))
    services.append(("bedrock-proxy-java", "127.0.0.1", 35572 if args.mac_tunnels else 25572))
    for name, host, port in services:
        if args.service and args.service != name:
            continue
        status = JavaServer(host, port, timeout=5).status(tries=1)
        print(json.dumps({
            "checkedAt": datetime.now(timezone.utc).isoformat(),
            "service": name,
            "online": status.players.online,
            "maximum": status.players.max,
            "version": status.version.name,
            "protocol": status.version.protocol,
        }, ensure_ascii=False), flush=True)
    if args.service and args.service != "bedrock-proxy-bedrock":
        return
    status = BedrockServer("100.64.0.43", 19132, timeout=5).status()
    print(json.dumps({
        "checkedAt": datetime.now(timezone.utc).isoformat(),
        "service": "bedrock-proxy-bedrock",
        "online": status.players.online,
        "maximum": status.players.max,
        "version": status.version.name,
        "protocol": status.version.protocol,
    }, ensure_ascii=False), flush=True)


if __name__ == "__main__":
    main()

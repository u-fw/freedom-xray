#!/usr/bin/env python3
"""Exercise user/binding accounting and reset against an isolated local Xray.

Run as root on Linux with XRAY_BIN and FREEDOM_BOT_BIN pointing to binaries.
The test expects Xray 26.10.31 or newer for Freedom's finalRules setting.
All listeners bind to loopback and all configuration is created in a temp dir.
"""

import fcntl
import http.server
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time


XRAY = os.environ["XRAY_BIN"]
BOT = os.environ["FREEDOM_BOT_BIN"]
ALICE_ID = "11111111-1111-4111-8111-111111111111"
BOB_ID = "22222222-2222-4222-8222-222222222222"


def unused_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_for_port(port, process):
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"Xray exited early: {process.stderr.read()}")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                return
        except OSError:
            time.sleep(0.05)
    raise RuntimeError(f"listener {port} did not start")


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        data = b"x" * 65536
        self.send_response(200)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *_args):
        pass


def start_xray(config, directory, name):
    path = directory / f"{name}.json"
    path.write_text(json.dumps(config), encoding="utf-8")
    return subprocess.Popen(
        [XRAY, "run", "-config", str(path)],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )


def main():
    if os.geteuid() != 0:
        raise SystemExit("local Bot maintenance commands require root")
    with tempfile.TemporaryDirectory(prefix="freedom-xray-integration-") as temp:
        directory = Path(temp)
        httpd = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=httpd.serve_forever, daemon=True)
        thread.start()
        ports = []
        while len(ports) < 6:
            port = unused_port()
            if port not in ports:
                ports.append(port)
        api_port, raw_port, cdn_port, alice_raw, alice_cdn, bob_raw = ports
        inbound = lambda tag, port, clients: {
            "tag": tag,
            "listen": "127.0.0.1",
            "port": port,
            "protocol": "vless",
            "settings": {"decryption": "none", "clients": clients},
        }
        server_config = {
            "log": {"loglevel": "info"},
            "stats": {},
            "api": {"tag": "api", "services": ["StatsService", "RoutingService"]},
            "dns": {"servers": ["localhost"], "hosts": {"freedom-integration.test": "127.0.0.1"}},
            "policy": {
                "levels": {"0": {"statsUserUplink": True, "statsUserDownlink": True}},
                "system": {"statsInboundUplink": True, "statsInboundDownlink": True},
            },
            "inbounds": [
                {"tag": "api-internal", "listen": "127.0.0.1", "port": api_port,
                 "protocol": "dokodemo-door", "settings": {"address": "127.0.0.1"}},
                inbound("raw", raw_port, [{"id": ALICE_ID, "email": "alice+cmF3"},
                                          {"id": BOB_ID, "email": "bob+cmF3"}]),
                inbound("cdn", cdn_port, [{"id": ALICE_ID, "email": "alice+Y2Ru"}]),
            ],
            "outbounds": [{"protocol": "freedom", "tag": tag, "settings": {"domainStrategy": "UseIP",
                "finalRules": [{"action": "allow", "network": "tcp", "ip": ["127.0.0.1/32"]}],
            }} for tag in ("direct", "direct2")] + [{"protocol": "vless", "tag": "proxy", "settings": {
                "address": "127.0.0.1", "port": raw_port, "id": ALICE_ID, "encryption": "none",
            }}],
            "routing": {"rules": [{"type": "field", "inboundTag": ["api-internal"],
                                   "outboundTag": "api"},
                                  {"type": "field", "domain": ["domain:example.com"],
                                   "outboundTag": "direct"},
                                  {"type": "field", "ip": ["127.0.0.1/32"],
                                   "balancerTag": "pool"}],
                        "balancers": [{"tag": "pool", "selector": ["direct"],
                                       "strategy": {"type": "roundRobin"}}]},
        }
        processes = []
        try:
            server = start_xray(server_config, directory, "config")
            processes.append(server)
            wait_for_port(api_port, server)
            for index, (socks_port, target_port, user_id) in enumerate([
                (alice_raw, raw_port, ALICE_ID),
                (alice_cdn, cdn_port, ALICE_ID),
                (bob_raw, raw_port, BOB_ID),
            ]):
                client = {
                    "log": {"loglevel": "info"},
                    "inbounds": [{"listen": "127.0.0.1", "port": socks_port,
                                  "protocol": "socks", "settings": {"udp": False}}],
                    "outbounds": [{"protocol": "vless", "settings": {"vnext": [{
                        "address": "127.0.0.1", "port": target_port,
                        "users": [{"id": user_id, "encryption": "none"}],
                    }]}}],
                }
                process = start_xray(client, directory, f"client-{index}")
                processes.append(process)
                wait_for_port(socks_port, process)
                subprocess.run([
                    "curl", "-fsS", "--max-time", "10", "--noproxy", "",
                    "--socks5-hostname", f"127.0.0.1:{socks_port}",
                    f"http://127.0.0.1:{httpd.server_port}/", "-o", os.devnull,
                ], check=True)
            subprocess.run([
                "curl", "-fsS", "--max-time", "10", "--noproxy", "",
                "--socks5-hostname", f"127.0.0.1:{alice_raw}",
                f"http://freedom-integration.test:{httpd.server_port}/", "-o", os.devnull,
            ], check=True)

            bot_config = directory / "bot.json"
            bot_config.write_text(json.dumps({
                "xray_config_path": str(directory / "config.json"),
                "xray_bin_path": XRAY,
                "stats_api_server": f"127.0.0.1:{api_port}",
            }), encoding="utf-8")

            def bot(*args):
                return subprocess.run([BOT, "-c", str(bot_config), *args], check=True,
                                      capture_output=True, text=True, timeout=40).stdout

            def agent(operation, input_data=None, *flags):
                result = subprocess.run(
                    [BOT, "agent", operation, "--config", str(bot_config), *flags],
                    input=None if input_data is None else json.dumps(input_data),
                    check=True, capture_output=True, text=True, timeout=40,
                )
                envelope = json.loads(result.stdout)
                assert envelope["ok"] and envelope["schema_version"] == 1, envelope
                return envelope["data"]

            original_config = (directory / "config.json").read_bytes()
            preview = agent("users.add", {"user": "preview-user", "node": "raw"})
            assert preview["applied"] is False, preview
            assert (directory / "config.json").read_bytes() == original_config
            routes = agent("routing.rules")
            assert routes[0]["protected"] and routes[1]["rule"]["domain"] == ["domain:example.com"], routes
            route_preview = agent("routing.rule.patch", {
                "index": routes[1]["index"], "etag": routes[1]["etag"],
                "field": "domain", "value": ["domain:changed.example"],
            })
            assert route_preview["applied"] is False, route_preview
            assert (directory / "config.json").read_bytes() == original_config
            balancers = agent("balancers.list")
            assert balancers[0]["tag"] == "pool" and balancers[0]["balancer"]["selector"] == ["direct"], balancers
            live = agent("balancers.live", {"tag": "pool"})
            assert live["tag"] == "pool" and set(live["selected"]) == {"direct", "direct2"}, live
            balance_preview = agent("balancers.patch", {
                "tag": "pool", "etag": balancers[0]["etag"],
                "field": "selector", "value": ["direct2"],
            })
            assert balance_preview["applied"] is False, balance_preview
            assert (directory / "config.json").read_bytes() == original_config
            outbounds = agent("outbounds.inspect")
            proxy = next(outbound for outbound in outbounds if outbound["tag"] == "proxy")
            assert proxy["endpoint"]["port"] == raw_port and ALICE_ID not in json.dumps(outbounds), outbounds
            outbound_preview = agent("outbounds.endpoint", {
                "index": proxy["index"], "etag": proxy["etag"], "field": "port", "value": "443",
            })
            assert outbound_preview["applied"] is False, outbound_preview
            assert (directory / "config.json").read_bytes() == original_config
            dns = agent("dns.view")
            assert dns["configured"] is True and dns["servers"][0]["address"] == "localhost", dns
            dns_preview = agent("dns.server.add", {"etag": dns["etag"], "value": "localhost"})
            assert dns_preview["applied"] is False, dns_preview
            hosts = agent("dns.hosts")
            assert hosts["hosts"][0]["name"] == "freedom-integration.test" and hosts["hosts"][0]["targets"] == ["127.0.0.1"] and hosts["etag"] == dns["etag"], hosts
            host_preview = agent("dns.host.add", {
                "etag": hosts["etag"], "name": "example.com", "targets": ["192.0.2.1"],
            })
            assert host_preview["applied"] is False, host_preview
            assert (directory / "config.json").read_bytes() == original_config
            assert {node["tag"] for node in agent("nodes.list")} == {"raw", "cdn"}
            users = agent("users.list")
            assert {user["user"] for user in users} == {"alice", "bob"}, users
            assert ALICE_ID not in json.dumps(users) and BOB_ID not in json.dumps(users)

            bot("-local-traffic-snapshot")
            before = json.loads(bot("-local-traffic-read"))
            assert agent("traffic.read")["users"]["alice"]["total_down"] >= 2 * 65536
            assert before["users"]["alice"]["total_down"] >= 2 * 65536, before
            assert before["users"]["bob"]["total_down"] >= 65536, before
            for binding in ("alice+cmF3", "alice+Y2Ru"):
                assert before["bindings"][binding]["total_down"] >= 65536, before

            with (directory / ".freedom-config.lock").open("w") as lock:
                fcntl.flock(lock, fcntl.LOCK_EX)
                bot("-local-user-traffic-reset-locked", "-local-user", "alice")
            bot("-local-traffic-snapshot")
            after = json.loads(bot("-local-traffic-read"))
            assert after["users"]["alice"]["total_down"] == 0, after
            assert after["users"]["bob"]["total_down"] == before["users"]["bob"]["total_down"], after
            for binding in ("alice+cmF3", "alice+Y2Ru"):
                assert after["bindings"][binding]["total_down"] == 0, after
            print("Real Xray DNS host resolution, balancer routing and live status, per-binding traffic and isolated user reset passed")
        except Exception:
            for index, process in enumerate(processes):
                process.terminate()
                stdout, stderr = process.communicate(timeout=5)
                print(f"Xray process {index}:\n{stdout}\n{stderr}")
            raise
        finally:
            httpd.shutdown()
            httpd.server_close()
            for process in reversed(processes):
                if process.poll() is not None:
                    continue
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


if __name__ == "__main__":
    main()

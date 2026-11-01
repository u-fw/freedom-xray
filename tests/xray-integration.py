#!/usr/bin/env python3
"""Exercise user/binding accounting and reset against an isolated local Xray.

Run as root on Linux with XRAY_BIN and FREEDOM_BOT_BIN pointing to binaries.
The test expects Xray 26.10.31 or newer for Freedom's finalRules setting.
All listeners bind to loopback and all configuration is created in a temp dir.
"""

import fcntl
import http.server
import ipaddress
import json
import os
from pathlib import Path
import socket
import subprocess
import struct
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


def query_fake_dns(port, process):
    labels = b"".join(bytes([len(label)]) + label.encode("ascii") for label in "freedom-fake.invalid".split(".")) + b"\x00"
    request = struct.pack("!HHHHHH", 0xF123, 0x0100, 1, 0, 0, 0) + labels + struct.pack("!HH", 1, 1)

    def skip_name(packet, offset):
        while True:
            size = packet[offset]
            offset += 1
            if size == 0:
                return offset
            if size & 0xC0 == 0xC0:
                return offset + 1
            offset += size

    deadline = time.monotonic() + 5
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        sock.settimeout(0.2)
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise RuntimeError("isolated FakeDNS core exited early")
            sock.sendto(request, ("127.0.0.1", port))
            try:
                response, _ = sock.recvfrom(4096)
            except socket.timeout:
                continue
            identifier, flags, questions, answers, _, _ = struct.unpack("!HHHHHH", response[:12])
            assert identifier == 0xF123 and flags & 0x000F == 0 and answers > 0
            offset = 12
            for _ in range(questions):
                offset = skip_name(response, offset) + 4
            for _ in range(answers):
                offset = skip_name(response, offset)
                record_type, record_class, _, length = struct.unpack("!HHIH", response[offset:offset + 10])
                offset += 10
                if record_type == 1 and record_class == 1 and length == 4:
                    return ipaddress.IPv4Address(response[offset:offset + 4])
                offset += length
    raise RuntimeError("isolated FakeDNS did not answer an A query")


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        data = b"x" * 65536
        self.send_response(200)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("X-Integration-Peer", self.client_address[0])
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
        while len(ports) < 9:
            port = unused_port()
            if port not in ports:
                ports.append(port)
        api_port, raw_port, cdn_port, alice_raw, alice_cdn, bob_raw, socks_port, vmess_port, vmess_client = ports
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
                {"tag": "socks-native", "listen": "127.0.0.1", "port": socks_port,
                 "protocol": "socks", "settings": {"auth": "password", "udp": False,
                 "accounts": [{"user": "native-test", "pass": "dummy-native-password"}]}},
                {"tag": "vmess-native", "listen": "127.0.0.1", "port": vmess_port,
                 "protocol": "vmess", "settings": {"clients": [{"id": ALICE_ID, "email": "alice"}]}},
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
        cdn_settings = server_config["inbounds"][2]["settings"]
        cdn_settings["users"] = cdn_settings.pop("clients")
        cdn_settings["clients"] = None
        # Distinct loopback source addresses let HTTP traffic prove which
        # outbound a manual runtime selection actually used.
        server_config["outbounds"][1]["sendThrough"] = "127.0.0.2"
        processes = []
        try:
            server = start_xray(server_config, directory, "config")
            processes.append(server)
            wait_for_port(api_port, server)
            wait_for_port(socks_port, server)
            subprocess.run([
                "curl", "-fsS", "--max-time", "10", "--noproxy", "",
                "--socks5-hostname", f"127.0.0.1:{socks_port}",
                "--proxy-user", "native-test:dummy-native-password",
                f"http://127.0.0.1:{httpd.server_port}/", "-o", os.devnull,
            ], check=True)
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
            vmess_config = {
                "log": {"loglevel": "info"},
                "inbounds": [{"listen": "127.0.0.1", "port": vmess_client,
                              "protocol": "socks", "settings": {"udp": False}}],
                "outbounds": [{"protocol": "vmess", "settings": {"vnext": [{
                    "address": "127.0.0.1", "port": vmess_port,
                    "users": [{"id": ALICE_ID, "alterId": 0, "security": "auto"}],
                }]}}],
            }
            vmess = start_xray(vmess_config, directory, "vmess-client")
            processes.append(vmess)
            wait_for_port(vmess_client, vmess)
            subprocess.run([
                "curl", "-fsS", "--max-time", "10", "--noproxy", "",
                "--socks5-hostname", f"127.0.0.1:{vmess_client}",
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
            full_rule = agent("routing.rule.get", {"index": routes[1]["index"], "etag": routes[1]["etag"]}, "--include-sensitive")
            assert full_rule["domain"] == ["domain:example.com"] and full_rule["outboundTag"] == "direct", full_rule
            route_preview = agent("routing.rule.patch", {
                "index": routes[1]["index"], "etag": routes[1]["etag"],
                "field": "domain", "value": ["domain:changed.example"],
            })
            assert route_preview["applied"] is False, route_preview
            assert (directory / "config.json").read_bytes() == original_config
            balancers = agent("balancers.list")
            assert balancers[0]["tag"] == "pool" and balancers[0]["balancer"]["selector"] == ["direct"], balancers
            full_balancer = agent("balancers.get", {"tag": "pool", "etag": balancers[0]["etag"]}, "--include-sensitive")
            assert full_balancer["strategy"]["type"] == "roundRobin", full_balancer
            live = agent("balancers.live", {"tag": "pool"})
            assert live["tag"] == "pool" and set(live["selected"]) == {"direct", "direct2"}, live
            assert set(live["override_targets"]) == {"direct", "direct2"} and len(live["state_etag"]) == 24, live
            force = {"tag": "pool", "state_etag": live["state_etag"], "target": "direct2"}
            assert not agent("balancers.override", force)["applied"]
            switched = agent("balancers.override", force, "--apply")
            assert switched["applied"] and switched["runtime_only"] and switched["status"]["override"] == "direct2", switched
            headers = subprocess.run([
                "curl", "-fsS", "--max-time", "10", "--noproxy", "",
                "--socks5-hostname", f"127.0.0.1:{alice_raw}",
                f"http://127.0.0.1:{httpd.server_port}/", "-o", os.devnull, "-D", "-",
            ], check=True, capture_output=True, text=True, timeout=15).stdout
            assert "X-Integration-Peer: 127.0.0.2" in headers, headers
            restored = agent("balancers.override", {
                "tag": "pool", "state_etag": switched["status"]["state_etag"], "target": "",
            }, "--apply")
            assert restored["applied"] and not restored["status"].get("override"), restored
            assert server.poll() is None and (directory / "config.json").read_bytes() == original_config
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
            assert {node["tag"] for node in agent("nodes.list")} == {"raw", "cdn", "socks-native", "vmess-native"}
            native_list = agent("inbounds.list")
            native = next(item for item in native_list["inbounds"] if item["tag"] == "socks-native")
            assert native["protocol"] == "socks" and "dummy-native-password" not in json.dumps(native_list), native_list
            native_ref = {"index": native["index"], "etag": native["etag"]}
            native_full = agent("inbounds.get", native_ref, "--include-sensitive")
            assert native_full["settings"]["accounts"][0]["pass"] == "dummy-native-password"
            native_patch = agent("inbounds.patch", {**native_ref, "field": "port", "value": "2200"})
            assert native_patch["applied"] is False, native_patch
            native_remove = agent("inbounds.remove", {**native_ref, "list_etag": native_list["list_etag"]})
            assert native_remove["applied"] is False and len(native_remove["preview"]["etag"]) == 24, native_remove
            node_deletion = agent("nodes.delete", {"tag": "raw"})
            assert node_deletion["applied"] is False and len(node_deletion["preview"]["etag"]) == 24, node_deletion
            assert (directory / "config.json").read_bytes() == original_config
            users = agent("users.list")
            assert {user["user"] for user in users} == {"alice", "bob"}, users
            alice = next(user for user in users if user["user"] == "alice")
            assert alice["traffic_ambiguous"] and set(alice["nodes"]) == {"raw", "cdn"}, alice
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
                failed_reset = subprocess.run(
                    [BOT, "-c", str(bot_config), "-local-user-traffic-reset-locked", "-local-user", "alice"],
                    capture_output=True, text=True, timeout=40,
                )
            assert failed_reset.returncode != 0 and "shares this statistics identity" in failed_reset.stderr

            vmess.terminate()
            vmess.wait(timeout=5)
            server.terminate()
            server.wait(timeout=5)
            server_config["inbounds"] = [item for item in server_config["inbounds"] if item["tag"] != "vmess-native"]
            server = start_xray(server_config, directory, "config")
            processes.append(server)
            wait_for_port(api_port, server)
            assert agent("users.get", {"user": "alice"})["traffic_ambiguous"]
            with (directory / ".freedom-config.lock").open("w") as lock:
                fcntl.flock(lock, fcntl.LOCK_EX)
                bot("-local-user-traffic-reset-locked", "-local-user", "alice")
            assert not agent("users.get", {"user": "alice"}).get("traffic_ambiguous", False)
            bot("-local-traffic-snapshot")
            after = json.loads(bot("-local-traffic-read"))
            assert after["users"]["alice"]["total_down"] == 0, after
            assert after["users"]["bob"]["total_down"] == before["users"]["bob"]["total_down"], after
            for binding in ("alice+cmF3", "alice+Y2Ru"):
                assert after["bindings"][binding]["total_down"] == 0, after

            # The test controls isolated Xray processes itself. A temporary
            # systemctl stub lets the manager's guarded apply path run without
            # touching any host service, then we start the resulting config.
            stub_dir = directory / "test-bin"
            stub_dir.mkdir()
            systemctl = stub_dir / "systemctl"
            systemctl.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
            systemctl.chmod(0o700)
            os.environ["PATH"] = str(stub_dir) + os.pathsep + os.environ.get("PATH", "")

            # Exercise native monitoring and strategy settings through the same
            # guarded CLI apply path; no host service is touched by this stub.
            monitor_config = {"subjectSelector": ["direct"], "probeInterval": "1m",
                              "probeURL": f"http://127.0.0.1:{httpd.server_address[1]}/", "enableConcurrency": True}
            assert agent("observatories.put", {"kind": "observatory", "config": monitor_config}, "--apply")["applied"]
            monitor = next(item for item in agent("observatories.list") if item["kind"] == "observatory")
            exported_monitor = agent("observatories.get", {"kind": "observatory", "etag": monitor["etag"]}, "--include-sensitive")
            assert exported_monitor == monitor_config, exported_monitor
            assert "127.0.0.1" not in json.dumps(monitor["config"]), monitor
            current_balancer = agent("balancers.list")[0]
            advanced_balancer = {"tag": "pool", "selector": ["direct"], "strategy": {
                "type": "leastLoad", "settings": {"expected": 1, "maxRTT": "2s",
                                                    "tolerance": 0.3, "baselines": ["100ms"],
                                                    "costs": [{"match": "direct", "value": 1}]}}}
            assert not agent("balancers.put", {"etag": current_balancer["etag"], "balancer": advanced_balancer})["applied"]
            assert agent("balancers.put", {"etag": current_balancer["etag"], "balancer": advanced_balancer}, "--apply")["applied"]
            current_balancer = agent("balancers.list")[0]
            assert agent("balancers.get", {"tag": "pool", "etag": current_balancer["etag"]}, "--include-sensitive") == advanced_balancer
            # Return the test's round-robin configuration before exercising traffic.
            assert agent("balancers.put", {"etag": current_balancer["etag"], "balancer": full_balancer}, "--apply")["applied"]
            assert agent("observatories.delete", {"kind": "observatory", "etag": monitor["etag"]}, "--apply")["applied"]
            native_ports = []
            while len(native_ports) < 4:
                port = unused_port()
                if port not in native_ports and port not in ports:
                    native_ports.append(port)
            native_vmess_port, native_trojan_port, vmess_socks, trojan_socks = native_ports
            server_config["inbounds"] += [
                {"tag": "generated-vmess", "listen": "127.0.0.1", "port": native_vmess_port,
                 "protocol": "vmess", "settings": {"clients": None, "users": []}},
                {"tag": "generated-trojan", "listen": "127.0.0.1", "port": native_trojan_port,
                 "protocol": "trojan", "settings": {"clients": None, "users": []}},
            ]
            (directory / "config.json").write_text(json.dumps(server_config), encoding="utf-8")
            credentials = {}
            for protocol in ("vmess", "trojan"):
                def client_ref():
                    entry = next(item for item in agent("inbounds.list")["inbounds"]
                                 if item["tag"] == "generated-" + protocol)
                    return {"index": entry["index"], "etag": entry["etag"]}

                ref = client_ref()
                add = {**ref, "value": "native-" + protocol}
                assert not agent("inbounds.clients.add", add)["applied"]
                assert agent("inbounds.clients.add", add, "--apply")["applied"]
                ref = client_ref()
                clients = agent("inbounds.clients.list", ref)
                assert len(clients["clients"]) == 1 and clients["clients"][0]["email"] == "native-" + protocol
                client_ref_data = {**ref, "client_index": 1}
                original = agent("inbounds.clients.export", client_ref_data, "--include-sensitive")
                secret_field = "id" if protocol == "vmess" else "password"
                assert original[secret_field] not in json.dumps(clients)
                assert agent("inbounds.clients.rotate", client_ref_data, "--apply")["applied"]
                ref = client_ref()
                client_ref_data = {**ref, "client_index": 1}
                rotated = agent("inbounds.clients.export", client_ref_data, "--include-sensitive")
                assert rotated[secret_field] != original[secret_field]
                credentials[protocol] = rotated[secret_field]
                assert agent("inbounds.clients.patch", {**client_ref_data, "field": "level", "value": "5"}, "--apply")["applied"]
                ref = client_ref()
                rename = {**ref, "client_index": 1, "field": "email", "value": "renamed-" + protocol}
                impact = agent("inbounds.clients.patch", rename)["preview"]
                assert agent("inbounds.clients.patch", {**rename, "routing_etag": impact["routing_etag"]}, "--apply")["applied"]

            def section_ref(name):
                entry = next(item for item in agent("xray.sections") if item["name"] == name)
                return {"name": name, "etag": entry["etag"]}

            for name, value in (
                ("log", {"loglevel": "warning", "access": "none"}),
                ("policy", {"levels": {"0": {"handshake": 8}}}),
                ("fakedns", [{"ipPool": "198.18.0.0/24", "poolSize": 64}]),
            ):
                change = {**section_ref(name), "value": value}
                assert not agent("xray.section.put", change)["applied"]
                assert agent("xray.section.put", change, "--apply")["applied"]
            policy = agent("xray.section.get", section_ref("policy"), "--include-sensitive")["value"]
            assert policy["levels"]["0"]["handshake"] == 8 and policy["levels"]["5"]["statsUserDownlink"]
            assert agent("xray.section.reset", section_ref("policy"), "--apply")["applied"]
            policy = agent("xray.section.get", section_ref("policy"), "--include-sensitive")["value"]
            assert "handshake" not in policy["levels"]["0"] and policy["levels"]["5"]["statsUserDownlink"]
            fake_summary = agent("xray.section.inspect", section_ref("fakedns"))
            assert fake_summary["summary"]["pools"][0]["poolSize"] == 64

            server.terminate()
            server.wait(timeout=5)
            server_config = json.loads((directory / "config.json").read_text(encoding="utf-8"))
            server = start_xray(server_config, directory, "config")
            processes.append(server)
            wait_for_port(native_vmess_port, server)
            wait_for_port(native_trojan_port, server)
            for protocol, listen_port, target_port in (("vmess", vmess_socks, native_vmess_port), ("trojan", trojan_socks, native_trojan_port)):
                if protocol == "vmess":
                    settings = {"vnext": [{"address": "127.0.0.1", "port": target_port,
                                           "users": [{"id": credentials[protocol], "alterId": 0, "security": "auto"}]}]}
                else:
                    settings = {"address": "127.0.0.1", "port": target_port, "password": credentials[protocol]}
                config = {"log": {"loglevel": "info"},
                          "inbounds": [{"listen": "127.0.0.1", "port": listen_port, "protocol": "socks"}],
                          "outbounds": [{"protocol": protocol, "settings": settings}]}
                process = start_xray(config, directory, "generated-client-" + protocol)
                processes.append(process)
                wait_for_port(listen_port, process)
                subprocess.run(["curl", "-fsS", "--max-time", "10", "--noproxy", "", "--socks5-hostname",
                                f"127.0.0.1:{listen_port}", f"http://127.0.0.1:{httpd.server_port}/", "-o", os.devnull], check=True)
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as reservation:
                reservation.bind(("127.0.0.1", 0))
                fake_dns_port = reservation.getsockname()[1]
            fake_pools = agent("xray.section.get", section_ref("fakedns"), "--include-sensitive")["value"]
            fake_config = {"log": {"loglevel": "warning"}, "fakedns": fake_pools,
                           "dns": {"servers": ["fakedns"], "queryStrategy": "UseIPv4"},
                           "inbounds": [{"tag": "fake-dns-input", "listen": "127.0.0.1", "port": fake_dns_port,
                                         "protocol": "dokodemo-door", "settings": {"address": "127.0.0.1", "port": 53, "network": "udp"}}],
                           "outbounds": [{"tag": "dns", "protocol": "dns", "settings": {}}]}
            fake_core = start_xray(fake_config, directory, "fake-dns-runtime")
            processes.append(fake_core)
            fake_ip = query_fake_dns(fake_dns_port, fake_core)
            assert fake_ip in ipaddress.ip_network("198.18.0.0/24"), fake_ip
            assert agent("xray.section.reset", section_ref("fakedns"), "--apply")["applied"]
            assert agent("xray.section.reset", section_ref("log"), "--apply")["applied"]
            print("Real Xray advanced sections, FakeDNS responses, native client CRUD, authenticated VMess/Trojan traffic, runtime balancing with verified egress and isolated traffic reset passed")
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

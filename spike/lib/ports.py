"""TCP port probes shared by the spike drivers.

spike/m5/router.py re-exports these names. spike/m6/run.sh calls this
file directly. 8317 and 8318 stay free for a host proxy.
"""

from __future__ import annotations

import socket

RESERVED = frozenset({8317, 8318})


def port_is_free(port: int) -> bool:
    """Return True when both 0.0.0.0 and 127.0.0.1 can bind port.

    macOS allows those two addresses to listen on one port at the same
    time. Binding only 0.0.0.0 can succeed while another process already
    owns 127.0.0.1. Each probe is closed before the next bind: on Linux
    a wildcard socket occupies the port for every address until then.
    """
    for host in ("0.0.0.0", "127.0.0.1"):
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        try:
            sock.bind((host, port))
        except OSError:
            return False
        finally:
            sock.close()
    return True


def loopback_accepts(port: int) -> bool:
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(0.5)
    try:
        sock.connect(("127.0.0.1", port))
    except OSError:
        return False
    else:
        return True
    finally:
        sock.close()


def ensure_port_available(port: int) -> None:
    if port in RESERVED:
        raise SystemExit(f"router port {port} is reserved")
    if loopback_accepts(port) or not port_is_free(port):
        raise SystemExit(f"router port {port} is answered by another process")


def pick_free_port() -> int:
    for _ in range(50):
        probe = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        try:
            probe.bind(("127.0.0.1", 0))
            port = int(probe.getsockname()[1])
        finally:
            probe.close()
        if port in RESERVED:
            continue
        if not loopback_accepts(port) and port_is_free(port):
            return port
    raise SystemExit("could not find a free router port")


def resolve_port(configured: str, env_name: str = "M5_ROUTER_PORT") -> int:
    text = configured.strip()
    if text == "":
        return pick_free_port()
    if not text.isdecimal():
        raise SystemExit(f"{env_name} must be a TCP port")
    port = int(text)
    if port < 1 or port > 65535:
        raise SystemExit(f"{env_name} must be a TCP port")
    ensure_port_available(port)
    return port

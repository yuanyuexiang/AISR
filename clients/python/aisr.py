"""Thin Python client for the AISR daemon (`aisr serve`).

Stdlib only. Connects to the daemon over a Unix socket (default ~/.aisr/aisr.sock)
or over TCP (base_url), and yields streamed turns as Event objects (NDJSON).

    from aisr import Client
    c = Client()                                   # ~/.aisr/aisr.sock
    # c = Client(base_url="http://host.docker.internal:7878", token="...")
    for ev in c.send("dev", "优化这个项目"):
        if ev.kind == "text":
            print(ev.text, end="", flush=True)

Environment defaults (overridden by explicit args): AISR_BASE_URL, AISR_SOCKET,
AISR_TOKEN — handy inside containers.
"""
from __future__ import annotations

import http.client
import json
import os
import socket
from dataclasses import dataclass
from typing import Any, Iterator, Optional
from urllib.parse import urlsplit


class AISRError(Exception):
    """A non-2xx response from the daemon."""

    def __init__(self, status: int, code: str, message: str):
        super().__init__(f"{message} ({code}, http {status})")
        self.status = status
        self.code = code
        self.message = message


@dataclass
class Event:
    kind: str
    text: str = ""
    raw: Any = None


@dataclass
class Session:
    name: str
    provider: str
    workspace: str
    provider_session: str
    created_at: str = ""
    updated_at: str = ""


class _UnixHTTPConnection(http.client.HTTPConnection):
    """HTTPConnection that dials a Unix domain socket."""

    def __init__(self, socket_path: str, timeout: Optional[float] = None):
        super().__init__("localhost", timeout=timeout)
        self._path = socket_path

    def connect(self) -> None:
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        if self.timeout is not None:
            s.settimeout(self.timeout)
        s.connect(self._path)
        self.sock = s


class Client:
    """Client for a local AISR daemon (Unix socket or TCP)."""

    def __init__(
        self,
        socket_path: Optional[str] = None,
        base_url: Optional[str] = None,
        token: Optional[str] = None,
        timeout: Optional[float] = None,
    ):
        base_url = base_url or os.environ.get("AISR_BASE_URL")
        self.token = token or os.environ.get("AISR_TOKEN")
        self.timeout = timeout
        if base_url:
            parts = urlsplit(base_url)
            self.base_url = base_url
            self._host = parts.hostname
            self._port = parts.port or 80
            self.socket_path = None
        else:
            if not hasattr(socket, "AF_UNIX"):
                raise RuntimeError(
                    "Unix domain sockets are unavailable on this platform "
                    "(e.g. native Windows Python). Use TCP instead, e.g. "
                    "Client(base_url='http://host.docker.internal:7878', token=...) "
                    "or set AISR_BASE_URL / AISR_TOKEN."
                )
            self.base_url = None
            self.socket_path = socket_path or os.environ.get("AISR_SOCKET") or os.path.expanduser("~/.aisr/aisr.sock")

    # --- session management ---

    def providers(self) -> list[dict]:
        return self._request("GET", "/v1/providers")["providers"]

    def create_session(self, provider: str = "claude", workspace: str = "", name: str = "") -> Session:
        body: dict[str, str] = {"provider": provider}
        if workspace:
            body["workspace"] = workspace
        if name:
            body["name"] = name
        return _to_session(self._request("POST", "/v1/sessions", body))

    def list_sessions(self) -> list[Session]:
        data = self._request("GET", "/v1/sessions")
        return [_to_session(s) for s in (data.get("sessions") or [])]

    def get_session(self, name: str) -> Session:
        return _to_session(self._request("GET", f"/v1/sessions/{name}"))

    def remove_session(self, name: str) -> None:
        self._request("DELETE", f"/v1/sessions/{name}")

    # --- chat ---

    def cancel(self, name: str) -> None:
        """Abort the in-flight turn for `name`. Raises AISRError (code
        NO_ACTIVE_TURN) if nothing is currently running for that session."""
        self._request("POST", f"/v1/sessions/{name}/cancel")

    def send(
        self,
        name: str,
        prompt: str,
        provider: str = "claude",
        workspace: str = "",
        model: str = "",
        agent: Optional[dict] = None,
    ) -> Iterator[Event]:
        """Run one turn against `name` (lazily created); yields Event objects.

        Pass ``agent`` (a dict, claude only) to drive an autonomous tool-using
        turn: {"append_system_prompt", "allowed_tools", "disallowed_tools",
        "mcp_config", "add_dirs", "max_turns", "permission_mode"}.
        """
        body: dict[str, Any] = {"prompt": prompt}
        if provider:
            body["provider"] = provider
        if workspace:
            body["workspace"] = workspace
        if model:
            body["model"] = model
        if agent:
            body["agent"] = agent

        conn = self._conn()
        conn.request("POST", f"/v1/sessions/{name}/messages",
                     body=json.dumps(body).encode(), headers=self._headers(json_body=True))
        resp = conn.getresponse()
        if resp.status >= 300:
            payload = resp.read()
            conn.close()
            _raise(resp.status, payload)
        try:
            for raw_line in resp:
                line = raw_line.strip()
                if not line:
                    continue
                obj = json.loads(line)
                yield Event(kind=obj.get("kind", ""), text=obj.get("text", ""), raw=obj.get("raw"))
        finally:
            conn.close()

    # --- internal ---

    def _conn(self) -> http.client.HTTPConnection:
        if self.base_url:
            return http.client.HTTPConnection(self._host, self._port, timeout=self.timeout)
        return _UnixHTTPConnection(self.socket_path, timeout=self.timeout)

    def _headers(self, json_body: bool = False) -> dict[str, str]:
        h: dict[str, str] = {}
        if json_body:
            h["Content-Type"] = "application/json"
        if self.token:
            h["Authorization"] = "Bearer " + self.token
        return h

    def _request(self, method: str, path: str, body: Optional[dict] = None):
        conn = self._conn()
        data = json.dumps(body).encode() if body is not None else None
        try:
            conn.request(method, path, body=data, headers=self._headers(json_body=body is not None))
            resp = conn.getresponse()
            payload = resp.read()
        finally:
            conn.close()
        if resp.status >= 300:
            _raise(resp.status, payload)
        return json.loads(payload) if payload else None


def _raise(status: int, payload: bytes) -> None:
    code, message = "INTERNAL", payload.decode(errors="replace")
    try:
        err = json.loads(payload).get("error", {})
        code = err.get("code", code)
        message = err.get("message", message)
    except Exception:
        pass
    raise AISRError(status, code, message)


def _to_session(d: dict) -> Session:
    return Session(
        name=d.get("name", ""),
        provider=d.get("provider", ""),
        workspace=d.get("workspace", ""),
        provider_session=d.get("provider_session", ""),
        created_at=d.get("created_at", ""),
        updated_at=d.get("updated_at", ""),
    )

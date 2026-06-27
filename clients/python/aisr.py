"""Thin Python client for the AISR daemon (`aisr serve`).

Stdlib only. Talks to the local Unix socket (default ~/.aisr/aisr.sock) over the
/v1 HTTP API; streamed turns are yielded as Event objects (NDJSON).

    from aisr import Client
    c = Client()
    for ev in c.send("dev", "优化这个项目"):
        if ev.kind == "text":
            print(ev.text, end="", flush=True)
"""
from __future__ import annotations

import http.client
import json
import os
import socket
from dataclasses import dataclass
from typing import Any, Iterator, Optional


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
    """Client for a local AISR daemon."""

    def __init__(self, socket_path: Optional[str] = None, timeout: Optional[float] = None):
        self.socket_path = socket_path or os.path.expanduser("~/.aisr/aisr.sock")
        self.timeout = timeout

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

    def send(
        self,
        name: str,
        prompt: str,
        provider: str = "claude",
        workspace: str = "",
        model: str = "",
    ) -> Iterator[Event]:
        """Run one turn against `name` (lazily created); yields Event objects."""
        body: dict[str, str] = {"prompt": prompt}
        if provider:
            body["provider"] = provider
        if workspace:
            body["workspace"] = workspace
        if model:
            body["model"] = model

        conn = self._conn()
        conn.request(
            "POST",
            f"/v1/sessions/{name}/messages",
            body=json.dumps(body).encode(),
            headers={"Content-Type": "application/json"},
        )
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

    def _conn(self) -> _UnixHTTPConnection:
        return _UnixHTTPConnection(self.socket_path, timeout=self.timeout)

    def _request(self, method: str, path: str, body: Optional[dict] = None):
        conn = self._conn()
        data = json.dumps(body).encode() if body is not None else None
        headers = {"Content-Type": "application/json"} if body is not None else {}
        try:
            conn.request(method, path, body=data, headers=headers)
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

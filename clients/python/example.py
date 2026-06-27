"""Minimal AISR Python client example.

Prerequisite: the daemon is running (`aisr serve`).

    python3 clients/python/example.py "用一句话回答:1+1等于几"
"""
import sys

from aisr import Client


def main() -> None:
    prompt = " ".join(sys.argv[1:]).strip() or "用一句话介绍你自己"
    c = Client()

    print("providers:", [p["name"] for p in c.providers()], file=sys.stderr)

    name = "py-demo"
    for ev in c.send(name, prompt):
        if ev.kind == "text":
            print(ev.text, end="", flush=True)
        elif ev.kind == "error":
            print(f"\n[error] {ev.text}", file=sys.stderr)
    print()

    s = c.get_session(name)
    print(f"session {s.name!r} -> provider session {s.provider_session}", file=sys.stderr)
    c.remove_session(name)


if __name__ == "__main__":
    main()

"""Report workflow jobs that carry no timeout-minutes ceiling.

Prints one line per unbounded job and nothing at all when every job is bounded,
so the calling gate needs no parsing beyond "was anything printed".
"""

import sys

import yaml


def unbounded(path: str) -> list[str]:
    doc = yaml.safe_load(open(path)) or {}
    out = []
    for name, job in (doc.get("jobs") or {}).items():
        if not isinstance(job, dict):
            continue
        # A job that only CALLS a reusable workflow cannot carry a timeout of
        # its own; the called workflow's jobs own one, and they are checked on
        # their own pass.
        if "uses" in job and "steps" not in job:
            continue
        if "timeout-minutes" not in job:
            out.append(name)
    return out


def main() -> None:
    for path in sys.argv[1:]:
        for name in unbounded(path):
            print(f"  MISSING  {path}: job {name} has no timeout-minutes")


if __name__ == "__main__":
    main()

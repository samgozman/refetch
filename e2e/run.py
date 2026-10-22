#!/usr/bin/env python3
"""Compatibility run: the same five claude -p sessions in four setups.

Setups: refetch alone, + RTK default, + RTK posthook, + context-mode. Each
session asks Claude to read one page of the local test server (e2e/server).
Pass: every page but the JavaScript-only one ends with its secret word in the
answer (the JavaScript page needs the headless Chrome step), and refetch
stats show the expected funnel.

Usage: python3 e2e/run.py [--model sonnet] [--setups alone,context-mode] [--out DIR]
Needs claude, go, and rtk for the RTK setups. Costs about 20 sessions.
"""
import argparse
import concurrent.futures as cf
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent.parent
CASES = {"ok": "apple", "403": "banana", "challenge": "damson", "js": "elder", "slow": "fig"}
CTX = "mcp__plugin_context-mode_context-mode__"
ALLOWED = ["WebFetch", "mcp__plugin_refetch_refetch__fetch_page",
           CTX + "ctx_fetch_and_index", CTX + "ctx_index", CTX + "ctx_search", CTX + "ctx_execute_file"]
RTK_POSTHOOK = """schema_version = 1
[pipeline]
name = "e2e-posthook"
extends = "rtk:posthook"
"""


def rtk_home(out: pathlib.Path) -> pathlib.Path:
    """An isolated HOME whose RTK runs the rtk:posthook pipeline."""
    home = out / "rtk-home"
    (home / ".claude").mkdir(parents=True, exist_ok=True)
    (home / "p.toml").write_text(RTK_POSTHOOK)
    env = {**os.environ, "HOME": str(home)}
    subprocess.run(["rtk", "pipeline", "add", "--force", str(home / "p.toml")], env=env, check=True, capture_output=True)
    return home


def settings(setup: str, out: pathlib.Path) -> dict:
    rtk = {"type": "command", "command": "rtk hook claude"}
    if setup == "alone":
        return {}
    if setup == "rtk-default":
        return {"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [rtk]}]}}
    if setup == "rtk-posthook":
        cmd = {"type": "command", "command": f"HOME='{rtk_home(out)}' rtk hook claude"}
        return {"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [cmd]}],
                          "PostToolUse": [{"matcher": "Bash|Read|Grep|Glob|WebFetch|WebSearch", "hooks": [cmd]}]}}
    if setup == "context-mode":
        return {"enabledPlugins": {"context-mode@context-mode": True}}
    raise SystemExit(f"unknown setup {setup}")


def session(setup, case, base, env, cfg, out, model):
    work = out / setup / "work"
    work.mkdir(parents=True, exist_ok=True)
    prompt = f"Read {base}/{case} and tell me the secret word on that page."
    cmd = ["claude", "-p", prompt, "--model", model, "--setting-sources", "project",
           "--plugin-dir", str(ROOT), "--settings", json.dumps(cfg),
           "--allowedTools", *ALLOWED, "--output-format", "stream-json", "--verbose"]
    res = subprocess.run(cmd, cwd=work, env=env, capture_output=True, text=True, timeout=600)
    (out / setup / f"{case}.stream.jsonl").write_text(res.stdout)
    tools, answer = [], ""
    for line in res.stdout.splitlines():
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        if ev.get("type") == "assistant":
            for block in ev.get("message", {}).get("content", []):
                if block.get("type") == "tool_use":
                    tools.append(block["name"].replace("mcp__plugin_refetch_refetch__", "").replace(CTX, "cm:"))
        if ev.get("type") == "result":
            answer = ev.get("result", "")
    return case, tools, CASES[case] in answer.lower(), answer


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="sonnet")
    ap.add_argument("--setups", default="alone,rtk-default,rtk-posthook,context-mode")
    ap.add_argument("--out", default=None)
    args = ap.parse_args()
    out = pathlib.Path(args.out or tempfile.mkdtemp(prefix="refetch-e2e-"))
    out.mkdir(parents=True, exist_ok=True)
    build = ROOT / "build"
    subprocess.run(["go", "build", "-tags", "e2e", "-o", str(build / "refetch-e2e"), "./cmd/refetch"], cwd=ROOT, check=True)
    subprocess.run(["go", "build", "-tags", "e2e", "-o", str(build / "e2e-server"), "./e2e/server"], cwd=ROOT, check=True)
    ca = out / "ca.pem"
    srv = subprocess.Popen([str(build / "e2e-server"), "-ca", str(ca)], stdout=subprocess.PIPE,
                           stderr=open(out / "server.log", "w"), text=True)
    base = "https://" + srv.stdout.readline().strip()
    print(f"server {base}, output in {out}")
    failed = False
    try:
        for setup in args.setups.split(","):
            data = out / setup / "data"
            if data.exists():
                shutil.rmtree(data)
            env = {**os.environ, "REFETCH_BIN": str(build / "refetch-e2e"), "REFETCH_DATA": str(data),
                   "REFETCH_CONFIG": str(out / "none.toml"), "NODE_EXTRA_CA_CERTS": str(ca)}
            cfg = settings(setup, out)
            with cf.ThreadPoolExecutor(len(CASES)) as pool:
                results = sorted(pool.map(lambda c: session(setup, c, base, env, cfg, out, args.model), CASES))
            st = json.loads(subprocess.run([str(build / "refetch-e2e"), "stats", "--json"], env=env,
                                           capture_output=True, text=True).stdout)
            print(f"\n## {setup}")
            for case, tools, ok, answer in results:
                expect = case != "js"
                mark = "ok  " if ok == expect else "FAIL"
                failed |= ok != expect
                print(f"  {mark} {case:10} answered={ok!s:5} tools={','.join(tools)}")
            keys = ["reads", "ok_first_time", "failed", "picked_up", "resolved", "still_failed", "not_picked_up",
                    "direct_fetch_page", "direct_fetch_page_ok"]
            print("  stats: " + " ".join(f"{k}={st.get(k, 0)}" for k in keys))
            print(f"         resolved_by={st.get('resolved_by')} still_failed_by={st.get('still_failed_by')}")
    finally:
        srv.terminate()
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()

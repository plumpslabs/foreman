#!/usr/bin/env python3
"""Foreman agent bridge: run one OpenHands SDK conversation for a task.

Protocol
--------
stdin : exactly one JSON object (the bridge config) terminated by a newline.
stdout: newline-delimited JSON events consumed by the Go runtime:
    {"type":"status"|"tool"|"message", "message":"..."}
    {"type":"result", "status":"completed|failed|blocked", "message":"...",
     "tokens":N, "cost":C}
stderr: diagnostics, captured by Foreman and reported on failure.

The Go side owns the timeout: it kills this process on cancel/deadline, so
no in-process timeout logic is needed here beyond the budget guard.
"""

import importlib
import json
import os
import sys
import threading
import time

_emit_lock = threading.Lock()


def emit(obj):
    with _emit_lock:
        try:
            sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
            sys.stdout.flush()
        except Exception:
            pass


def fail(message, status="failed"):
    emit({
        "type": "result",
        "status": status,
        "message": str(message)[:4000],
        "tokens": 0,
        "cost": 0.0,
    })
    sys.exit(0)


def read_config():
    line = sys.stdin.readline()
    if not line:
        fail("bridge received no configuration from Foreman")
    try:
        return json.loads(line)
    except Exception as exc:
        fail("bridge configuration is not valid JSON: %s" % exc)


def load_tool(module_name, class_name):
    """Import an OpenHands tool class and return a Tool instance, or None."""
    try:
        module = importlib.import_module(module_name)
        tool_cls = getattr(module, class_name)
        from openhands.sdk import Tool
        return Tool(name=tool_cls.name)
    except Exception as exc:
        emit({"type": "status", "message": "tool %s unavailable: %s" % (class_name, exc)})
        return None


def project_overview(root, max_entries=400):
    """Shallow file listing so the agent starts with a map of the workspace."""
    skip_dirs = {".git", "node_modules", ".venv", "venv", "__pycache__",
                 "dist", "build", "target", ".foreman", ".idea", ".vscode"}
    entries = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in skip_dirs and not d.startswith(".")]
        rel = os.path.relpath(dirpath, root)
        for name in sorted(filenames):
            if name.startswith(".") and name not in (".env.example",):
                continue
            path = name if rel == "." else os.path.join(rel, name)
            entries.append(path)
            if len(entries) >= max_entries:
                entries.append("... (truncated)")
                return "\n".join(entries)
    return "\n".join(entries)


def read_metric(obj, names):
    for name in names:
        try:
            value = getattr(obj, name)
        except Exception:
            continue
        if isinstance(value, (int, float)):
            return float(value)
        if isinstance(value, dict):
            nested = read_metric(value, names)
            if nested:
                return nested
    return 0.0


def collect_usage(llm):
    """Read token/cost totals from the SDK's Metrics object."""
    try:
        metrics = llm.metrics
    except Exception:
        return 0, 0.0
    cost = 0.0
    tokens = 0
    try:
        cost = float(getattr(metrics, "accumulated_cost", 0) or 0)
    except Exception:
        cost = 0.0
    try:
        usage = getattr(metrics, "accumulated_token_usage", None)
        if usage is not None:
            tokens = int(getattr(usage, "prompt_tokens", 0) or 0) + int(
                getattr(usage, "completion_tokens", 0) or 0
            )
    except Exception:
        tokens = 0
    if not tokens:
        tokens = int(read_metric(metrics, ("total_tokens", "total_tokens_used")) or 0)
    return tokens, cost


def extract_final_message(conversation):
    """Best-effort: last agent-authored text from the event log."""
    try:
        log = conversation.state.events
    except Exception:
        return ""
    items = []
    for accessor in ("get_all_events", "events", "_events", "list_events"):
        try:
            value = getattr(log, accessor)
            value = value() if callable(value) else value
            if value:
                items = list(value)
                break
        except Exception:
            continue
    for event in reversed(items):
        try:
            source = getattr(event, "source", None)
            content = getattr(event, "content", None)
            kind = type(event).__name__.lower()
            if isinstance(content, str) and content.strip():
                if "message" in kind and source != "user":
                    return content.strip()[:4000]
                if source == "agent":
                    return content.strip()[:4000]
        except Exception:
            continue
    return ""


def main():
    config = read_config()
    workspace = config.get("workspace") or os.getcwd()
    if not os.path.isdir(workspace):
        fail("workspace does not exist: %s" % workspace)

    try:
        from openhands.sdk import LLM, Agent, Conversation
    except Exception as exc:
        fail(
            "OpenHands SDK is not installed in this Python environment. "
            "Install it with: pip install openhands-sdk openhands-tools "
            "(original import error: %s)" % exc
        )

    emit({"type": "status", "message": "OpenHands engine loaded, preparing workspace"})

    tools = []
    requested = config.get("tools") or []
    for spec in requested:
        if not isinstance(spec, dict):
            continue
        module = spec.get("module")
        class_name = spec.get("class")
        if not module or not class_name:
            continue
        tool = load_tool(module, class_name)
        if tool is not None:
            tools.append(tool)
    if not tools:
        file_tool = load_tool("openhands.tools.file_editor", "FileEditorTool")
        if file_tool is None:
            fail("OpenHands file editor tool could not be loaded; the engine install is incomplete")
        tools.append(file_tool)
        tracker = load_tool("openhands.tools.task_tracker", "TaskTrackerTool")
        if tracker is not None:
            tools.append(tracker)
    emit({"type": "status", "message": "Loaded %d tools" % len(tools)})

    model = (config.get("model") or "").strip()
    protocol = config.get("protocol") or "openai-compatible"
    if model and "/" not in model:
        prefix = "anthropic" if protocol == "anthropic" else "openai"
        model = "%s/%s" % (prefix, model)
    llm_kwargs = {"model": model, "api_key": config.get("apiKey") or None}
    if config.get("baseUrl"):
        llm_kwargs["base_url"] = config["baseUrl"]
    try:
        llm = LLM(**llm_kwargs)
    except Exception as exc:
        fail("could not configure the model provider: %s" % exc)

    agent = Agent(llm=llm, tools=tools)
    max_iterations = int(config.get("maxIterations") or 30)
    budget = float(config.get("budgetUsd") or 0)
    # visualizer=None keeps the SDK's pretty-printer off stdout so the
    # JSON-line protocol stays the only thing on the pipe.
    conv_kwargs = {"visualizer": None}
    if budget > 0:
        conv_kwargs["max_budget_per_run"] = budget
    try:
        conversation = Conversation(
            agent=agent,
            workspace=workspace,
            max_iteration_per_run=max_iterations,
            **conv_kwargs,
        )
    except TypeError:
        try:
            conversation = Conversation(
                agent=agent, workspace=workspace, max_iterations=max_iterations, **conv_kwargs
            )
        except TypeError:
            conversation = Conversation(agent=agent, workspace=workspace, **conv_kwargs)
            try:
                conversation.state.max_iterations = max_iterations
            except Exception:
                pass

    prompt = config.get("prompt") or ""
    overview = project_overview(workspace)
    if overview:
        prompt += "\n\nWorkspace file overview:\n" + overview

    state = {"over_budget": False, "done": False, "last_status": ""}

    def poll():
        while not state["done"]:
            try:
                status = str(conversation.state.execution_status)
                if status and status.lower() != state["last_status"]:
                    state["last_status"] = status.lower()
                    emit({"type": "status", "message": "engine: %s" % status.lower()})
            except Exception:
                pass
            if budget > 0 and not state["over_budget"]:
                _, cost = collect_usage(llm)
                if cost > budget:
                    state["over_budget"] = True
                    emit({"type": "status",
                          "message": "budget limit reached ($%.4f > $%.4f); pausing run" % (cost, budget)})
                    try:
                        conversation.pause()
                    except Exception:
                        pass
            time.sleep(0.75)

    poller = threading.Thread(target=poll, daemon=True)
    poller.start()

    conversation.send_message(prompt)
    run_error = None
    try:
        conversation.run()
    except Exception as exc:
        run_error = exc
    state["done"] = True
    poller.join(timeout=2)

    tokens, cost = collect_usage(llm)
    final_message = extract_final_message(conversation)

    if state["over_budget"]:
        status = "blocked"
        message = final_message or ("Run paused after reaching the configured budget of $%.4f." % budget)
    elif run_error is not None:
        status = "failed"
        message = "Engine run failed: %s" % run_error
    else:
        status = "completed"
        message = final_message or "Agent run finished."

    emit({"type": "result", "status": status, "message": message,
          "tokens": tokens, "cost": cost})


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as exc:
        import traceback
        traceback.print_exc(file=sys.stderr)
        fail("bridge crashed: %s" % exc)

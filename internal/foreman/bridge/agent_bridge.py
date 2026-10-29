#!/usr/bin/env python3
"""Foreman agent bridge: run one OpenHands SDK conversation for a task.

Protocol
--------
stdin : exactly one JSON object (the bridge config) terminated by a newline.
stdout: newline-delimited JSON events consumed by the Go runtime:
    {"type":"status"|"tool"|"message", "message":"..."}
    {"type":"result", "status":"completed|failed|blocked|paused", "message":"...",
     "tokens":N, "cost":C}

A SIGTERM from Foreman pauses the run at the next step boundary: the
conversation state is persisted (persistenceDir) and the result reports
"paused" so the task can be continued later without losing progress.
stderr: diagnostics, captured by Foreman and reported on failure.

The Go side owns the timeout: it kills this process on cancel/deadline, so
no in-process timeout logic is needed here beyond the budget guard.
"""

import importlib
import json
import os
import signal
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


def extract_final_message(conversation, limit=4000):
    """Best-effort: the last agent-authored text from the event log.

    Falls back to any recent non-user textual content (tool summaries, status
    lines) so a completed run still reports something useful when the final
    agent message is empty or was condensed away.
    """
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
    fallback = []
    for event in reversed(items):
        try:
            source = getattr(event, "source", None)
            content = getattr(event, "content", None)
            kind = type(event).__name__.lower()
            if isinstance(content, str) and content.strip():
                if "message" in kind and source != "user":
                    return content.strip()[:limit]
                if source == "agent":
                    return content.strip()[:limit]
                if source != "user":
                    fallback.append(content.strip())
        except Exception:
            continue
        if len(fallback) >= 3:
            break
    if fallback:
        return "\n".join(reversed(fallback))[:limit]
    return ""


def main():
    config = read_config()
    workspace = config.get("workspace") or os.getcwd()
    if not os.path.isdir(workspace):
        fail("workspace does not exist: %s" % workspace)

    # Pause bookkeeping is set up immediately so a SIGTERM that arrives during
    # startup (model load, tool import) is still recorded, not a hard kill.
    state = {"over_budget": False, "done": False, "last_status": "", "paused": False}

    def request_pause(reason):
        state["paused"] = True
        emit({"type": "status", "message": "pausing run (%s); conversation state is saved" % reason})
        try:
            conversation.pause()
        except Exception:
            pass

    # Foreman pauses a run by sending SIGTERM: stop at the next step boundary
    # and keep the persisted conversation so the task can be continued later.
    def on_sigterm(signum, frame):
        request_pause("pause requested")

    try:
        signal.signal(signal.SIGTERM, on_sigterm)
    except Exception:
        pass

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
    emit({"type": "status", "message": "LLM ready: model=%s base_url=%s" % (model, config.get("baseUrl") or "default")})

    agent = Agent(llm=llm, tools=tools)
    # Context compaction: the SDK's summarizing condenser keeps long runs from
    # blowing the model window (same defaults as the SDK's default agent).
    try:
        from openhands.sdk import default_condenser
        agent.condenser = default_condenser(llm)
        emit({"type": "status", "message": "Context condenser enabled"})
    except Exception as exc:
        emit({"type": "status", "message": "condenser unavailable: %s" % exc})
    max_iterations = int(config.get("maxIterations") or 30)
    budget = float(config.get("budgetUsd") or 0)
    # Conversation state (events, agent progress) persists to disk so a paused
    # or interrupted run resumes where it stopped instead of starting over.
    persistence_dir = (config.get("persistenceDir") or "").strip()
    conversation_id = (config.get("conversationId") or "").strip()
    if persistence_dir:
        try:
            os.makedirs(persistence_dir, exist_ok=True)
        except Exception as exc:
            emit({"type": "status", "message": "persistence unavailable: %s" % exc})
    resumed = False

    def build_conversation():
        """Construct a Conversation, always disabling the visualizer (stdout
        must stay clean for the JSON-line protocol) and stepping down optional
        kwargs so SDK version differences never mask real TypeErrors.
        """
        base = {"agent": agent, "workspace": workspace, "visualizer": None}
        attempts = [
            dict(base,
                 max_iterations=max_iterations,
                 max_budget_per_run=budget,
                 persistence_dir=persistence_dir,
                 conversation_id=conversation_id),
            dict(base, max_iterations=max_iterations, persistence_dir=persistence_dir,
                 conversation_id=conversation_id),
            dict(base, max_iterations=max_iterations, persistence_dir=persistence_dir),
            dict(base, max_iterations=max_iterations),
            dict(base),
        ]
        last = None
        for attempt in attempts:
            # Skip attempts that reference unset options.
            if attempt.get("max_budget_per_run") is not None and attempt["max_budget_per_run"] <= 0:
                attempt.pop("max_budget_per_run", None)
            if attempt.get("persistence_dir") is None:
                attempt.pop("persistence_dir", None)
                attempt.pop("conversation_id", None)
            try:
                return Conversation(**attempt)
            except TypeError as exc:
                last = exc
            except Exception:
                raise
        raise last

    try:
        conversation = build_conversation()
    except Exception as exc:
        fail("could not create the conversation: %s" % exc)
    try:
        conversation.state.max_iterations = max_iterations
    except Exception:
        pass
    if persistence_dir:
        try:
            prior = list(conversation.state.events.get_all_events())
            resumed = len(prior) > 0
        except Exception:
            resumed = False
        if resumed:
            emit({"type": "status", "message": "Resuming persisted conversation"})

    prompt = config.get("prompt") or ""
    overview = project_overview(workspace)
    if overview:
        prompt += "\n\nWorkspace file overview:\n" + overview

    # If a pause arrived during startup, honor it without starting the run.
    if state["paused"]:
        tokens, cost = collect_usage(llm)
        emit({"type": "result", "status": "paused", "message": "Run paused before it started.",
              "tokens": tokens, "cost": cost})
        return

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
                    request_pause("budget limit reached")
            time.sleep(0.75)

    poller = threading.Thread(target=poll, daemon=True)
    poller.start()

    if resumed:
        conversation.send_message(
            "Continue the previous run from where it stopped. Re-check the current "
            "workspace state before acting, then finish the task described below.\n\n" + prompt
        )
    else:
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

    # If the agent loop ended in an error/stuck state, surface that as a failed
    # run instead of a completed run with an empty message.
    terminal_state = ""
    try:
        terminal_state = str(conversation.state.execution_status).lower()
    except Exception:
        pass

    if state["paused"] and not state["over_budget"]:
        status = "paused"
        message = final_message or "Run paused. Conversation state is saved for Continue."
    elif state["over_budget"]:
        status = "blocked"
        message = final_message or ("Run paused after reaching the configured budget of $%.4f." % budget)
    elif run_error is not None:
        status = "failed"
        message = "Engine run failed: %s" % run_error
    elif terminal_state in ("error", "stuck"):
        status = "failed"
        message = final_message or ("Agent loop ended in state %r." % terminal_state)
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

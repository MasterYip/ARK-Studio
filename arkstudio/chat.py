"""Project chat harness.

AR K ships with a local, deterministic harness so the Chat tab works with no
model/API key. When DeepSeek Harness is available, set ``ARK_DSH_COMMAND`` (for
example ``dsh headless -``) and the same chat is delegated to DSH with the
project folder as its working directory. The local fallback still uses the same
tool vocabulary and writes through ``Store``; every write is an ordinary ARK
activity, visible in the graph.
"""
from __future__ import annotations

import html
import json
import os
import re
import shlex
import shutil
import subprocess
import urllib.error
import urllib.request
from typing import Any

try:
    import winreg  # Windows: DEEPSEEK_API_KEY may live in the User environment
except ImportError:  # pragma: no cover - non-Windows
    winreg = None

from .store import Store, StoreError
from . import assistant_config, importer
from .tags import KNOWN_STATUSES, KNOWN_TYPES, RELATIONS

TYPE_WORDS = {
    "idea": "idea", "想法": "idea", "点子": "idea",
    "task": "task", "todo": "task", "任务": "task", "待办": "task",
    "claim": "claim", "论点": "claim", "主张": "claim",
    "question": "question", "问题": "question",
    "method": "method", "方法": "method",
    "experiment": "experiment", "实验": "experiment", "实验设计": "experiment",
    "related work": "related_work", "related_work": "related_work", "相关工作": "related_work",
    "story": "story", "section": "story", "故事": "story", "章节": "story",
    "source": "source", "来源": "source",
    "note": "note", "笔记": "note",
}

TYPE_LABEL = {
    "idea": "idea", "task": "task", "claim": "claim", "question": "question",
    "method": "method", "experiment": "experiment", "related_work": "related work",
    "story": "story section", "source": "source", "note": "note",
}


def _env_value(name: str) -> str:
    value = (os.environ.get(name) or "").strip()
    if value or winreg is None:
        return value
    # The server may have been started from a shell that does not inherit the
    # Windows User environment. Read the User Environment key without copying
    # it into project files or logs.
    for hive in (winreg.HKEY_CURRENT_USER, winreg.HKEY_LOCAL_MACHINE):
        try:
            with winreg.OpenKey(hive, r"Environment") as key:
                value, _ = winreg.QueryValueEx(key, name)
                if isinstance(value, str) and value.strip():
                    return value.strip()
        except OSError:
            continue
    return ""


def deepseek_config() -> dict:
    return assistant_config.load()


def harness_status(config: dict | None = None) -> dict:
    forced = _env_value("ARK_CHAT_MODE").lower()
    config = config if config is not None else deepseek_config()
    dsh = (os.environ.get("ARK_DSH_COMMAND") or "").strip()
    if forced == "local":
        dsh = ""
    if dsh and forced != "deepseek":
        parts = shlex.split(dsh, posix=False)
        available = bool(parts and (shutil.which(parts[0]) or os.path.isabs(parts[0])))
        if available:
            return {
                "mode": "dsh",
                "label": "DeepSeek Harness",
                "command": dsh,
                "available": True,
                "note": "Writes through the project API; runs with the .ark folder as cwd.",
            }
        return {
            "mode": "local",
            "label": "Local ARK harness",
            "command": dsh,
            "available": True,
            "note": "ARK_DSH_COMMAND is configured but not on PATH; using the local harness.",
        }
    if config["api_key"] and forced != "local":
        return {
            "mode": "deepseek",
            "label": f"DeepSeek API · {config['model']}",
            "model": config["model"],
            "reasoning_effort": config["reasoning_effort"],
            "available": True,
            "note": "OpenAI-compatible API from Settings → Assistant API; server-side tool calling with reasoning.",
        }
    return {
        "mode": "local",
        "label": "Local ARK harness",
        "available": True,
        "note": "No DEEPSEEK_API_KEY / DSH command is configured, so this is the deterministic local tool parser, not a model.",
    }


class DeepSeekError(ValueError):
    pass


def _deepseek_chat(messages: list[dict], tools: list[dict] | None, config: dict) -> dict:
    body = {
        "model": config["model"],
        "messages": messages,
        "reasoning_effort": config["reasoning_effort"],
        "temperature": 0.2,
        "max_tokens": 3000,
        "stream": False,
    }
    if tools:
        body["tools"] = tools
        body["tool_choice"] = "auto"
    request = urllib.request.Request(
        config["base_url"] + "/chat/completions",
        data=json.dumps(body, ensure_ascii=False).encode("utf-8"),
        headers={
            "Authorization": "Bearer " + config["api_key"],
            "Content-Type": "application/json",
            "Accept": "application/json",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=config["timeout"]) as response:
            data = json.load(response)
    except urllib.error.HTTPError as exc:
        detail = exc.read(800).decode("utf-8", "replace").strip()
        raise DeepSeekError(f"HTTP {exc.code}: {detail or exc.reason}") from exc
    except (urllib.error.URLError, TimeoutError, OSError, ValueError) as exc:
        raise DeepSeekError(str(exc)) from exc
    try:
        return data["choices"][0]["message"]
    except (KeyError, IndexError, TypeError) as exc:
        raise DeepSeekError("DeepSeek returned an unexpected response") from exc


def _deepseek_stream(messages: list[dict], tools: list[dict] | None, config: dict):
    body = {
        "model": config["model"], "messages": messages,
        "reasoning_effort": config["reasoning_effort"], "temperature": 0.2,
        "max_tokens": 3000, "stream": True,
    }
    if tools:
        body.update(tools=tools, tool_choice="auto")
    request = urllib.request.Request(
        config["base_url"] + "/chat/completions",
        data=json.dumps(body, ensure_ascii=False).encode("utf-8"),
        headers={"Authorization": "Bearer " + config["api_key"],
                 "Content-Type": "application/json", "Accept": "text/event-stream"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=config["timeout"]) as response:
            finished = False
            for raw in response:
                line = raw.decode("utf-8").strip()
                if not line.startswith("data:"):
                    continue
                payload = line[5:].strip()
                if payload == "[DONE]":
                    finished = True
                    break
                chunk = json.loads(payload)
                if chunk.get("error"):
                    raise DeepSeekError(str(chunk["error"].get("message") or "AI endpoint error"))
                for choice in chunk.get("choices") or []:
                    if choice.get("index", 0) != 0:
                        continue
                    yield choice.get("delta") or {}
                    finished = finished or choice.get("finish_reason") is not None
            if not finished:
                raise DeepSeekError("AI stream ended before the assistant finished")
    except urllib.error.HTTPError as exc:
        detail = exc.read(800).decode("utf-8", "replace").strip()
        raise DeepSeekError(f"HTTP {exc.code}: {detail or exc.reason}") from exc
    except (urllib.error.URLError, TimeoutError, OSError, ValueError) as exc:
        raise DeepSeekError(str(exc)) from exc


def _stream_safe_content(raw: str) -> str:
    # Gate partial legacy DSML markers across network chunks before exposing text.
    marker = re.search(r"<[\s/]*[|｜]", raw)
    if marker:
        return raw[:marker.start()]
    start = raw.rfind("<")
    if start >= 0:
        tail = re.sub(r"\s", "", raw[start + 1:]).lstrip("/")
        if re.fullmatch(r"[|｜]{0,2}(?:D(?:S(?:M(?:L)?)?)?)?", tail):
            return raw[:start]
    return raw


def stream_chat(store: Store, project, message: str, *, agent_token: str = "",
                base_url: str = "", infohub: dict | None = None,
                config: dict | None = None, history_user: str | None = None):
    """Forward provider deltas and tool progress; return the same result as run_chat."""
    config = config if config is not None else deepseek_config()
    status = harness_status(config)
    yield {"type": "status", "phase": "thinking", "harness": status["label"]}
    if status["mode"] != "deepseek":
        result = run_chat(store, project, message, agent_token=agent_token,
                          base_url=base_url, infohub=infohub, config=config,
                          history_user=history_user)
        if result.get("reasoning"):
            yield {"type": "reasoning", "text": result["reasoning"]}
        yield {"type": "content", "text": result.get("message") or ""}
        return result
    messages = [{"role": "system", "content": _system_prompt(store, project, infohub)}]
    for item in store.chat_history(24, user=history_user):
        if item.get("role") in ("user", "assistant"):
            text = _strip_dsml(str(item.get("content") or ""))
            if text:
                messages.append({"role": item["role"], "content": text[:6000]})
    if not any(m["role"] == "user" and m["content"] == message for m in messages[1:]):
        messages.append({"role": "user", "content": message})
    used, reasoning, answer, refresh = [], "", "", False
    for round_index in range(8):
        yield {"type": "status", "phase": "thinking"}
        raw, sent, round_reasoning, calls = "", "", "", {}
        prefix = "\n\n" if answer else ""
        for delta in _deepseek_stream(messages, _tool_specs() if round_index < 7 else None, config):
            if delta.get("reasoning_content"):
                text = str(delta["reasoning_content"])
                if not round_reasoning and reasoning:
                    text = "\n\n" + text
                round_reasoning += text
                reasoning += text
                yield {"type": "reasoning", "text": text}
            if delta.get("content"):
                raw += str(delta["content"])
                safe = _stream_safe_content(raw)
                if len(safe) > len(sent):
                    text = (prefix if not sent else "") + safe[len(sent):]
                    sent = safe
                    answer += text
                    yield {"type": "content", "text": text}
            for call in delta.get("tool_calls") or []:
                slot = calls.setdefault(call.get("index", 0), {
                    "id": "", "type": "function", "function": {"name": "", "arguments": ""}})
                if call.get("id"):
                    slot["id"] = call["id"]
                for key in ("name", "arguments"):
                    slot["function"][key] += (call.get("function") or {}).get(key) or ""
        legacy_calls, clean = _parse_dsml_tool_calls(raw)
        tool_calls = list(calls.values()) or legacy_calls
        # The safe prefix can hold a literal trailing '<' until the round finishes.
        if len(clean) > len(sent) and clean.startswith(sent):
            text = (prefix if not sent else "") + clean[len(sent):]
            answer += text
            yield {"type": "content", "text": text}
        if not tool_calls:
            if not answer.strip():
                answer = "The model returned no answer. Please try again."
                yield {"type": "content", "text": answer}
            return {"message": answer, "reasoning": reasoning[-24000:],
                    "tools": used, "refresh": refresh, "suggestions": _suggestions(),
                    "harness": status["label"]}
        assistant_message = {"role": "assistant", "content": clean, "tool_calls": tool_calls}
        if round_reasoning:
            assistant_message["reasoning_content"] = round_reasoning.lstrip("\n")
        messages.append(assistant_message)
        for index, call in enumerate(tool_calls):
            function = call.get("function") or {}
            name = function.get("name") or ""
            try:
                args = json.loads(function.get("arguments") or "{}")
            except (ValueError, TypeError):
                args = {}
            if not isinstance(args, dict):
                args = {}
            tool_id = call.get("id") or f"tool-{round_index}-{index}"
            call["id"] = tool_id
            yield {"type": "tool", "id": tool_id, "name": name, "args": args, "state": "running"}
            result, did_write = _execute_tool(store, name, args, infohub)
            refresh = refresh or did_write
            tool = {"name": name, "args": args, "ok": "error" not in result, "result": result}
            used.append(tool)
            yield {"type": "tool", "id": tool_id, "state": "done", **tool}
            messages.append({"role": "tool", "tool_call_id": tool_id,
                             "content": json.dumps(result, ensure_ascii=False)[:12000]})
    ending = "I reached the tool-call limit before finishing. Please ask me to continue."
    yield {"type": "content", "text": "\n\n" + ending}
    return {"message": answer + "\n\n" + ending, "reasoning": reasoning[-24000:],
            "tools": used, "refresh": refresh, "suggestions": _suggestions(),
            "harness": status["label"]}


def _tool_specs() -> list[dict]:
    return [
        {"type": "function", "function": {
            "name": "get_context",
            "description": "Read current ARK project stats, next actions and recent units.",
            "parameters": {"type": "object", "properties": {}, "additionalProperties": False},
        }},
        {"type": "function", "function": {
            "name": "search_units",
            "description": "Search ARK units by title/body/uid, optionally by type.",
            "parameters": {"type": "object", "properties": {
                "query": {"type": "string"},
                "type": {"type": "string"},
                "limit": {"type": "integer", "minimum": 1, "maximum": 20},
            }, "required": ["query"], "additionalProperties": False},
        }},
        {"type": "function", "function": {
            "name": "list_units",
            "description": "List/filter any ARK units by type, status, importance or free-text query.",
            "parameters": {"type": "object", "properties": {
                "type": {"type": "string", "enum": sorted(KNOWN_TYPES)},
                "status": {"type": "string", "enum": sorted(KNOWN_STATUSES)},
                "importance": {"type": "string", "enum": ["p0", "p1", "p2", "p3"]},
                "query": {"type": "string"},
                "limit": {"type": "integer", "minimum": 1, "maximum": 50},
            }, "additionalProperties": False},
        }},
        {"type": "function", "function": {
            "name": "get_unit",
            "description": "Read one ARK unit and its links by uid or numeric id.",
            "parameters": {"type": "object", "properties": {"ref": {"type": "string"}},
                           "required": ["ref"], "additionalProperties": False},
        }},
        {"type": "function", "function": {
            "name": "create_unit",
            "description": "Create an ARK unit. Use only when the user asks to create/capture something.",
            "parameters": {"type": "object", "properties": {
                "type": {"type": "string", "enum": sorted(KNOWN_TYPES)},
                "title": {"type": "string"},
                "body": {"type": "string"},
                "status": {"type": "string", "enum": sorted(KNOWN_STATUSES)},
                "importance": {"type": "string", "enum": ["p0", "p1", "p2", "p3"]},
                "tags": {"type": "array", "items": {"type": "string"}},
            }, "required": ["type", "title"], "additionalProperties": False},
        }},
        {"type": "function", "function": {
            "name": "update_unit",
            "description": "Update title/status/importance/tags of an existing ARK unit.",
            "parameters": {"type": "object", "properties": {
                "ref": {"type": "string"},
                "title": {"type": "string"},
                "status": {"type": "string", "enum": sorted(KNOWN_STATUSES)},
                "importance": {"type": "string", "enum": ["p0", "p1", "p2", "p3"]},
                "tags": {"type": "array", "items": {"type": "string"}},
            }, "required": ["ref"], "additionalProperties": False},
        }},
        {"type": "function", "function": {
            "name": "link_units",
            "description": "Create a directed link between two ARK units.",
            "parameters": {"type": "object", "properties": {
                "source": {"type": "string"},
                "target": {"type": "string"},
                "relation": {"type": "string", "enum": sorted(RELATIONS)},
                "note": {"type": "string"},
            }, "required": ["source", "target", "relation"], "additionalProperties": False},
        }},
        {"type": "function", "function": {
            "name": "get_infohub_context",
            "description": "Read the current InfoHub graph scope: database path, active UI filter rules, node count and matching InfoHub topics. Use this before answering questions about what is currently visible in InfoHub.",
            "parameters": {"type": "object", "properties": {}, "additionalProperties": False},
        }},
        {"type": "function", "function": {
            "name": "search_infohub",
            "description": "Search the read-only InfoHub database by text, kind, tags, status or recent days. Returns topic keys and summaries.",
            "parameters": {"type": "object", "properties": {
                "query": {"type": "string"},
                "kind": {"type": "string"},
                "tags": {"type": "string"},
                "status": {"type": "string"},
                "days": {"type": "integer", "minimum": 0},
                "limit": {"type": "integer", "minimum": 1, "maximum": 100},
            }, "additionalProperties": False},
        }},
        {"type": "function", "function": {
            "name": "get_infohub_unit",
            "description": "Read one InfoHub topic, its hierarchy, tags and a compact plain-text conversation sample.",
            "parameters": {"type": "object", "properties": {"key": {"type": "string"}},
                           "required": ["key"], "additionalProperties": False},
        }},
    ]


def _infohub_path(context: dict | None) -> str:
    context = context if isinstance(context, dict) else {}
    path = str(context.get("path") or "").strip()
    if path:
        return path
    candidates = importer.default_candidates()
    if not candidates:
        raise StoreError("No InfoHub database found; set INFOHUB_DB or the InfoHub path")
    return candidates[0]["path"]


def _infohub_filters(context: dict | None) -> dict:
    context = context if isinstance(context, dict) else {}
    filters = context.get("filters") if isinstance(context.get("filters"), dict) else {}
    return filters


def _infohub_graph(store: Store, context: dict | None, *, limit: int = 2000) -> dict:
    filters = _infohub_filters(context)
    source = importer.InfoHubSource(_infohub_path(context))
    try:
        return source.graph(
            store,
            q=str(filters.get("q") or ""),
            kind=",".join(str(item) for item in (filters.get("kinds") or [])),
            tags=",".join(str(item) for item in (filters.get("tags") or [])),
            status=str(filters.get("status") or ""),
            days=int(filters.get("days") or 0),
            mine=bool(filters.get("mine")),
            roots=bool(filters.get("roots")),
            order=str(filters.get("order") or "activity"),
            connected=bool(filters.get("connected", True)),
            context=bool(filters.get("context")),
            limit=min(2000, max(1, limit)),
        )
    finally:
        source.close()


def _execute_tool(store: Store, name: str, args: dict,
                  infohub: dict | None = None) -> tuple[dict, bool]:
    args = args if isinstance(args, dict) else {}
    write = name in {"create_unit", "update_unit", "link_units"}
    try:
        if name == "get_context":
            stats = store.stats()
            actions = store.next_actions()
            recent = store.list_units({"limit": 10, "sort": "updated"})["items"]
            return {"stats": stats, "next_actions": actions, "recent": [
                {"uid": u["uid"], "type": u["type"], "title": u["title"], "status": u["status"]}
                for u in recent
            ]}, False
        if name == "search_units":
            filters = {"q": str(args.get("query") or ""), "limit": min(20, max(1, int(args.get("limit") or 8)))}
            if args.get("type"):
                filters["types"] = [str(args["type"])]
            data = store.list_units(filters)
            return {"total": data["total"], "items": [
                {"uid": u["uid"], "type": u["type"], "title": u["title"], "status": u["status"]}
                for u in data["items"]
            ]}, False
        if name == "list_units":
            filters = {"limit": min(50, max(1, int(args.get("limit") or 20))), "sort": "updated"}
            if args.get("type"):
                filters["types"] = [str(args["type"])]
            if args.get("status"):
                filters["statuses"] = [str(args["status"])]
            if args.get("importance"):
                filters["importance"] = [str(args["importance"])]
            if args.get("query"):
                filters["q"] = str(args["query"])
            data = store.list_units(filters)
            return {"total": data["total"], "items": [
                {"uid": u["uid"], "type": u["type"], "title": u["title"], "status": u["status"], "importance": u["importance"]}
                for u in data["items"]
            ]}, False
        if name == "get_unit":
            unit = store.get_unit(str(args.get("ref") or ""))
            return {"unit": {k: unit.get(k) for k in ("uid", "id", "type", "title", "body", "status", "importance", "tags")},
                    "links_out": unit.get("links_out", [])[:20], "links_in": unit.get("links_in", [])[:20]}, False
        if name == "create_unit":
            unit = store.create_unit({
                "type": args.get("type") or "note",
                "title": args.get("title") or "Untitled",
                "body": args.get("body") or "",
                "status": args.get("status") or "open",
                "importance": args.get("importance") or "p2",
                "tags": list(args.get("tags") or []) + ["origin:chat"],
                "origin": "assistant",
            }, actor="assistant:deepseek")
            return {"uid": unit["uid"], "id": unit["id"], "title": unit["title"], "type": unit["type"]}, True
        if name == "update_unit":
            patch = {k: args[k] for k in ("title", "status", "importance", "tags") if k in args}
            unit = store.update_unit(str(args.get("ref") or ""), patch, actor="assistant:deepseek")
            return {"uid": unit["uid"], "title": unit["title"], "status": unit["status"]}, True
        if name == "link_units":
            link = store.create_link(str(args.get("source") or ""), str(args.get("target") or ""),
                                     str(args.get("relation") or "relates_to"),
                                     str(args.get("note") or "Created by DeepSeek assistant"),
                                     actor="assistant:deepseek")
            return {"id": link["id"], "source": link["source"], "target": link["target"], "relation": link["relation"]}, True
        if name == "get_infohub_context":
            data = _infohub_graph(store, infohub)
            nodes = [
                {
                    "key": node.get("uid") or node.get("key"),
                    "kind": node.get("kind"),
                    "title": node.get("title"),
                    "status": node.get("status"),
                    "summary": str(node.get("summary") or "")[:240],
                    "messages": node.get("n_messages"),
                    "imported": node.get("imported"),
                }
                for node in data.get("nodes", [])[:80]
            ]
            topics = [
                {"key": node.get("uid") or node.get("key"),
                 "kind": node.get("kind"), "title": str(node.get("title") or "")[:100]}
                for node in data.get("nodes", [])[:1000]
            ]
            return {
                "path": data.get("path"),
                "visible_in_ui": bool((infohub or {}).get("visible", True)),
                "filters": _infohub_filters(infohub),
                "total": data.get("total"),
                "matched_count": data.get("matched_count"),
                "context_count": data.get("context_count"),
                "kinds": data.get("kinds"),
                "nodes": nodes,
                "topics": topics,
                "shown": len(nodes),
                "topics_shown": len(topics),
                "edges": len(data.get("edges", [])),
            }, False
        if name == "search_infohub":
            source = importer.InfoHubSource(_infohub_path(infohub))
            try:
                data = source.browse(
                    store,
                    q=str(args.get("query") or ""),
                    kind=str(args.get("kind") or ""),
                    tags=str(args.get("tags") or ""),
                    status=str(args.get("status") or ""),
                    days=int(args.get("days") or 0),
                    limit=min(100, max(1, int(args.get("limit") or 20))),
                )
            finally:
                source.close()
            return {
                "total": data.get("total"),
                "items": [
                    {"key": item.get("key"), "kind": item.get("kind"), "title": item.get("title"),
                     "status": item.get("status"), "summary": str(item.get("summary") or "")[:300],
                     "tags": item.get("tags", [])[:8]}
                    for item in data.get("items", [])
                ],
            }, False
        if name == "get_infohub_unit":
            source = importer.InfoHubSource(_infohub_path(infohub))
            try:
                unit = source.unit(str(args.get("key") or ""), store)
            finally:
                source.close()
            messages = [
                {"sender": msg.get("sender_name") or msg.get("sender"), "text": str(msg.get("text") or "")[:400]}
                for msg in (unit.get("messages") or [])[-20:]
            ]
            return {
                "key": unit.get("key"), "kind": unit.get("kind"), "title": unit.get("title"),
                "summary": unit.get("summary"), "status": unit.get("status"),
                "tags": unit.get("tags"), "parents": unit.get("parents"),
                "children": unit.get("children"), "messages": messages,
                "n_messages": unit.get("n_messages"), "imported": unit.get("imported"),
            }, False
        return {"error": f"Unknown tool: {name}"}, False
    except (StoreError, ValueError) as exc:
        return {"error": str(exc)}, False


_DSML_TAG = r"<\s*/?\s*[|｜]\s*[|｜]\s*DSML\s*[|｜]\s*[|｜]\s*"


def _strip_dsml(text: str) -> str:
    value = str(text or "")
    if "DSML" not in value:
        return value.strip()
    # Remove complete calls/invoke/parameter wrappers first.
    value = re.sub(rf"{_DSML_TAG}calls\s*>.*?{_DSML_TAG}calls\s*>", "", value, flags=re.S | re.I)
    value = re.sub(rf"{_DSML_TAG}invoke\s*[^>]*>.*?{_DSML_TAG}invoke\s*>", "", value, flags=re.S | re.I)
    # Any remaining DSML tag or lone marker is implementation detail.
    value = re.sub(rf"{_DSML_TAG}[^>]*>", "", value, flags=re.I)
    value = re.sub(r"<\s*/?\s*[|｜]\s*[|｜]\s*DSML[^>\n]*>?", "", value, flags=re.I)
    value = re.sub(r"^\s*[|｜]\s*[|｜]\s*DSML.*$", "", value, flags=re.M | re.I)
    return re.sub(r"\n{3,}", "\n\n", value).strip()


def clean_message(text: str) -> str:
    """Remove model-internal DSML markup from stored/displayed chat text."""
    return _strip_dsml(text)


def _parse_dsml_tool_calls(text: str) -> tuple[list[dict], str]:
    """Fallback for gateways that return V4 DSML markup in content."""
    value = str(text or "")
    if "DSML" not in value:
        return [], value
    invoke_re = re.compile(
        rf"{_DSML_TAG}invoke\s+name\s*=\s*[\"']([^\"']+)[\"'][^>]*>(.*?)"
        rf"{_DSML_TAG}invoke\s*>",
        re.S | re.I,
    )
    parameter_re = re.compile(
        rf"{_DSML_TAG}parameter\s+name\s*=\s*[\"']([^\"']+)[\"']([^>]*)>(.*?)"
        rf"{_DSML_TAG}parameter\s*>",
        re.S | re.I,
    )
    calls = []
    for index, match in enumerate(invoke_re.finditer(value)):
        name = match.group(1).strip()
        args = {}
        for param in parameter_re.finditer(match.group(2)):
            key = param.group(1).strip()
            attrs, raw = param.group(2), param.group(3)
            raw = re.sub(r"<[^>]+>", "", raw)
            raw = html.unescape(raw).strip()
            if re.search(r"string\s*=\s*[\"']true[\"']", attrs, re.I):
                args[key] = raw
            else:
                try:
                    args[key] = json.loads(raw)
                except ValueError:
                    args[key] = raw
        calls.append({
            "id": f"dsml-{index}",
            "type": "function",
            "function": {"name": name, "arguments": json.dumps(args, ensure_ascii=False)},
        })
    return calls, _strip_dsml(value)


def _system_prompt(store: Store, project, infohub: dict | None = None) -> str:
    stats = store.stats()
    actions = store.next_actions()
    recent = store.list_units({"limit": 12, "sort": "updated"})["items"]
    lines = [
        "You are ARK Studio's project assistant, working on the current ARK project.",
        "Think through the request before acting. Use the provided tools to inspect or modify the project.",
        "Do not invent UIDs, units or links. Search/read first when identifiers are unclear.",
        "Only create/update/link when the user asks for a change. Keep answers concise and concrete.",
        "ARK is the source of truth: use get_context, list_units, search_units and get_unit to inspect any project data. When you mention a unit, include its UID (for example S-0004); the UI makes UIDs clickable.",
        "Respond in the user's language unless they ask otherwise. Use Markdown.",
        "",
        f"Project: {project.name}",
        f"Project path: {project.path}",
        f"Stats: {stats['total']} units, {stats['links']} links, {stats['tasks']['open']} open tasks.",
        f"Types: {json.dumps(stats['by_type'], ensure_ascii=False)}",
    ]
    if actions:
        lines.append("Next actions: " + "; ".join(a["title"] for a in actions[:6]))
    if recent:
        lines.append("Recent units: " + "; ".join(f"{u['uid']} {u['title']}" for u in recent[:10]))
    if infohub:
        try:
            path = _infohub_path(infohub)
            filters = _infohub_filters(infohub)
            lines.append(
                "InfoHub is available at " + path + ". Current InfoHub graph filters: "
                + json.dumps(filters, ensure_ascii=False)
                + f" (visible in UI: {bool(infohub.get('visible', True))})."
            )
            lines.append("Use get_infohub_context to see the topics currently in the InfoHub graph, "
                         "search_infohub to search the read-only database, and get_infohub_unit for details.")
        except (StoreError, ValueError):
            pass
    lines.append("Use at most 4-6 tool calls; do not repeatedly read the same unit. After inspecting enough evidence, answer the user directly.")
    return "\n".join(lines)


def _run_deepseek(store: Store, project, text: str, status: dict,
                  infohub: dict | None = None,
                  config: dict | None = None, history_user: str | None = None) -> dict:
    config = config if config is not None else deepseek_config()
    if not config["api_key"]:
        raise DeepSeekError("DEEPSEEK_API_KEY is not configured")
    messages = [{"role": "system", "content": _system_prompt(store, project, infohub)}]
    for message in store.chat_history(24, user=history_user):
        if message.get("role") not in ("user", "assistant"):
            continue
        content = _strip_dsml(str(message.get("content") or "")).strip()
        if content:
            messages.append({"role": message["role"], "content": content[:6000]})
    if not any(m["role"] == "user" and m["content"] == text for m in messages[1:]):
        messages.append({"role": "user", "content": text})
    tools = _tool_specs()
    used, reasoning, refresh = [], "", False
    for round_index in range(8):
        # Give the model a final tool-free turn to synthesize after exploration.
        message = _deepseek_chat(messages, tools if round_index < 7 else None, config)
        content = str(message.get("content") or "").strip()
        if message.get("reasoning_content"):
            reasoning = str(message["reasoning_content"])[-12000:]
        tool_calls = message.get("tool_calls") or []
        if not tool_calls:
            tool_calls, content = _parse_dsml_tool_calls(content)
        content = _strip_dsml(content)
        if not tool_calls:
            return {
                "message": content or "I updated the project.",
                "tools": used,
                "refresh": refresh,
                "suggestions": _suggestions(),
                "harness": status["label"],
                "reasoning": reasoning,
            }
        assistant_message = {"role": "assistant", "content": content or ""}
        assistant_message["tool_calls"] = tool_calls
        messages.append(assistant_message)
        for call in tool_calls:
            function = call.get("function") or {}
            name = function.get("name") or ""
            try:
                args = json.loads(function.get("arguments") or "{}")
            except ValueError:
                args = {}
            result, did_write = _execute_tool(store, name, args, infohub)
            refresh = refresh or did_write
            used.append({"name": name, "args": args, "ok": "error" not in result,
                         "result": result})
            messages.append({"role": "tool", "tool_call_id": call.get("id") or name,
                             "content": json.dumps(result, ensure_ascii=False)[:12000]})
    return {
        "message": "I reached the tool-call limit before finishing. Check the tools above and ask me to continue.",
        "tools": used,
        "refresh": refresh,
        "suggestions": _suggestions(),
        "harness": status["label"],
        "reasoning": reasoning,
    }


def run_chat(store: Store, project, message: str, actor: str = "assistant:chat",
             agent_token: str = "", base_url: str = "", infohub: dict | None = None,
             config: dict | None = None, history_user: str | None = None) -> dict:
    text = str(message or "").strip()
    if not text:
        raise ValueError("message is required")
    status = harness_status(config)
    if status["mode"] == "dsh":
        result = _run_dsh(store, project, text, status, agent_token=agent_token,
                          base_url=base_url, infohub=infohub)
        if result is not None:
            return result
    if status["mode"] == "deepseek":
        try:
            return _run_deepseek(store, project, text, status, infohub=infohub,
                                 config=config, history_user=history_user)
        except DeepSeekError as exc:
            return _reply(
                f"DeepSeek API error: {exc}\n\nCheck `DEEPSEEK_API_KEY`, `DEEPSEEK_BASE_URL` and `DEEPSEEK_MODEL`.",
                suggestions=_suggestions(),
            )
    result = _run_local(store, text, config=config)
    if status["mode"] == "dsh":
        result["harness"] = "Local ARK harness (DSH fallback)"
    return result


# ---------------------------------------------------------------- local harness
def _run_local(store: Store, text: str, *, config: dict | None = None) -> dict:
    normalized = text.strip().strip("。.!！?？")
    lowered = normalized.lower()

    if re.search(r"^(help|what can you do|commands|帮助|你能做什么|怎么用)", lowered):
        return _reply(_help_text(), suggestions=_suggestions())

    if re.search(r"^(summary|status|overview|project status|what should i do next|next steps|"
                 r"总结|状态|进度|下一步|接下来)", lowered):
        return _summary(store)

    link = _parse_link(normalized)
    if link:
        return _link(store, *link)

    status = _parse_status(normalized)
    if status:
        return _set_status(store, *status)

    dispatch = _parse_dispatch(normalized)
    if dispatch:
        return _dispatch(store, dispatch)

    search = _parse_search(normalized)
    if search:
        return _search(store, search)

    create = _parse_create(normalized)
    if create:
        return _create(store, *create)

    # A question is allowed to have no tool call; give it project context.
    words = [w for w in re.findall(r"[\w\u4e00-\u9fff]{2,}", lowered) if w not in _STOP]
    found = store.list_units({"q": " ".join(words[:4]), "limit": 6}) if words else {"items": [], "total": 0}
    if found["items"]:
        titles = "\n".join(f"- **{u['uid']}** {u['title']} ({TYPE_LABEL.get(u['type'], u['type'])})" for u in found["items"])
        return _reply(
            f"I searched the project for your message.\n\n{titles}\n\n"
            "I can also create, link, mark, or dispatch nodes. Say `help` for the command list.",
            suggestions=_suggestions(),
        )
    missing_key = ""
    status = harness_status(config)
    effective_config = config if config is not None else deepseek_config()
    dsh_command = (os.environ.get("ARK_DSH_COMMAND") or "").strip()
    if (status.get("mode") == "local"
            and not str(effective_config.get("api_key") or "").strip()
            and not dsh_command):
        missing_key = (
            "**No model API key is configured.** This reply comes from the local "
            "deterministic harness, not a language model. Add an API key in "
            "Settings → Assistant API for model-backed replies.\n\n"
        )
    return _reply(
        missing_key
        + "I don't have a matching tool for that yet. I can work directly on this project:\n\n"
        + _help_text(),
        suggestions=_suggestions(),
    )


_STOP = {
    "the", "and", "for", "with", "this", "that", "from", "about", "what", "how",
    "can", "you", "please", "help", "project", "the", "ark", "一个", "帮我", "可以", "项目",
}


def _reply(message: str, tools: list | None = None, *, refresh=False, suggestions=None) -> dict:
    return {
        "message": message,
        "tools": tools or [],
        "refresh": refresh,
        "suggestions": suggestions or [],
        "harness": harness_status()["label"],
    }


def _tool(name: str, args: dict, result: Any = None, ok: bool = True) -> dict:
    return {"name": name, "args": args, "ok": ok, "result": result}


def _help_text() -> str:
    return (
        "**Project tools**\n"
        "- `create idea: title | body` (also task, claim, question, method, experiment, related work)\n"
        "- `link T-0001 supports C-0001` (any ARK relation)\n"
        "- `mark T-0001 done`\n"
        "- `search scaling`\n"
        "- `dispatch T-0001`\n"
        "- `summary` or `what should I do next`\n\n"
        "Use `Ctrl+J` from anywhere to open this chat."
    )


def _suggestions() -> list[str]:
    return [
        "What should I do next?",
        "Create idea: A learned recovery policy | Test it against the fixed schedule.",
        "Search scaling",
    ]


def _summary(store: Store) -> dict:
    stats = store.stats()
    actions = store.next_actions()
    lines = [
        f"**{stats['total']} units · {stats['links']} links · {stats['tasks']['open']} open tasks**",
        f"{stats['by_type'].get('idea', 0)} ideas · {stats['by_type'].get('task', 0)} tasks · "
        f"{stats['by_type'].get('claim', 0)} claims · {stats['by_type'].get('experiment', 0)} experiments",
    ]
    if actions:
        lines.append("\n**Needs attention**")
        for action in actions[:5]:
            lines.append(f"- {action['title']} — {action['detail']}")
    return _reply("\n".join(lines), suggestions=_suggestions())


def _parse_create(text: str):
    m = re.match(
        r"^(?:create|add|new|make|capture)\s+(?:an?\s+|a\s+)?"
        r"(idea|task|todo|claim|question|method|experiment|related work|related_work|story|section|source|note)"
        r"\b(?:\s*(?:about|on|for|titled|:|-))?\s*(.*)$",
        text, re.I | re.S,
    )
    if m:
        return _type_from_word(m.group(1)), m.group(2).strip()
    m = re.match(r"^(idea|task|todo|claim|question|method|experiment|source|note)\s*[:\-]\s*(.+)$",
                 text, re.I | re.S)
    if m:
        return _type_from_word(m.group(1)), m.group(2).strip()
    m = re.match(
        r"^(?:新建|创建|添加|记下|记录|加)(?:一个|一条|个)?\s*"
        r"(想法|点子|任务|待办|论点|主张|问题|方法|实验设计|实验|相关工作|故事|章节|来源|笔记)?"
        r"[\s:：，,]*(.+)$", text, re.S,
    )
    if m:
        return _type_from_word(m.group(1) or "idea"), m.group(2).strip()
    return None


def _type_from_word(word: str) -> str:
    return TYPE_WORDS.get((word or "").strip().lower(), "idea")


def _create(store: Store, type_name: str, value: str) -> dict:
    if not value:
        return _reply("Tell me what the node should say, for example `create idea: Use a learned recovery policy`.")
    title, body = value, ""
    if "|" in value:
        title, body = [part.strip() for part in value.split("|", 1)]
    elif "\n" in value:
        title, body = [part.strip() for part in value.split("\n", 1)]
    title = title.strip().strip("。.")[:240]
    due = None
    due_match = re.search(r"\bdue\s+(\d{4}-\d{2}-\d{2})\b", body or title, re.I)
    if due_match:
        due = due_match.group(1)
        title = re.sub(r"\s*\bdue\s+\d{4}-\d{2}-\d{2}\b", "", title, flags=re.I)
    try:
        unit = store.create_unit(
            {
                "type": type_name,
                "title": title,
                "body": body,
                "status": "open",
                "importance": "p2",
                "end_at": due,
                "origin": "assistant",
                "tags": ["origin:chat"],
            },
            actor="assistant:chat",
        )
    except StoreError as exc:
        return _reply(f"I couldn't create that node: {exc}", [_tool("create_unit", {"type": type_name}, str(exc), False)])
    label = TYPE_LABEL.get(type_name, type_name)
    return _reply(
        f"Created **{unit['uid']}** — {unit['title']} ({label}).",
        [_tool("create_unit", {"type": type_name, "title": title}, {"uid": unit["uid"], "id": unit["id"]})],
        refresh=True,
        suggestions=["Open it in the inspector", "Link it to a claim"],
    )


def _parse_search(text: str) -> str | None:
    m = re.match(r"^(?:search|find|look for|show me|grep|查找|搜索|找)\s*[:：]?\s*(.+)$", text, re.I | re.S)
    return m.group(1).strip() if m else None


def _search(store: Store, query: str) -> dict:
    data = store.list_units({"q": query, "limit": 8})
    if not data["items"]:
        return _reply(f"No nodes matched `{query}`.", suggestions=_suggestions())
    lines = [f"Found **{data['total']}** nodes for `{query}`:"]
    for unit in data["items"]:
        lines.append(f"- **{unit['uid']}** {unit['title']} · {TYPE_LABEL.get(unit['type'], unit['type'])} · {unit['status']}")
    return _reply("\n".join(lines), [_tool("search_units", {"query": query}, {"total": data["total"]})])


def _parse_link(text: str):
    clean = lambda value: value.strip(" .,;:，。；：")
    relations = "|".join(sorted(RELATIONS, key=len, reverse=True))
    m = re.search(
        rf"\b([A-Za-z]+-\d+|\d+)\s+({relations})\s+([A-Za-z]+-\d+|\d+)\b",
        text, re.I,
    )
    if m:
        return clean(m.group(1)), clean(m.group(3)), m.group(2).lower()
    m = re.search(
        r"\b(?:link|connect|relate)\s+([A-Za-z]+-\d+|\d+)\s+(?:to\s+|with\s+)?"
        r"([A-Za-z]+-\d+|\d+)(?:\s+(?:as|via|with)\s+([a-z_]+))?",
        text, re.I,
    )
    if m:
        relation = (m.group(3) or "relates_to").lower()
        if relation not in RELATIONS:
            relation = "relates_to"
        return clean(m.group(1)), clean(m.group(2)), relation
    return None


def _link(store: Store, source: str, target: str, relation: str) -> dict:
    try:
        link = store.create_link(source, target, relation, "Created from chat", actor="assistant:chat")
    except StoreError as exc:
        return _reply(f"I couldn't create that link: {exc}", [_tool("link_units", {"source": source, "target": target, "relation": relation}, str(exc), False)])
    return _reply(
        f"Linked **{source}** → **{target}** as `{relation}`.",
        [_tool("link_units", {"source": source, "target": target, "relation": relation}, {"id": link["id"]})],
        refresh=True,
    )


def _parse_status(text: str):
    m = re.search(r"^(?:mark|set|update)\s+([A-Za-z]+-\d+|\d+)\s+(?:as\s+|to\s+)?([a-z_]+)$", text, re.I)
    if m:
        return m.group(1), m.group(2).lower()
    m = re.search(r"^(?:把|将)?\s*([A-Za-z]+-\d+|\d+)\s*(?:标记为|设为|改成|状态改为)\s*([a-z_]+)$", text, re.I)
    return (m.group(1), m.group(2).lower()) if m else None


def _set_status(store: Store, ref: str, status: str) -> dict:
    if status not in KNOWN_STATUSES:
        return _reply(f"`{status}` isn't an ARK status. Try: {', '.join(sorted(KNOWN_STATUSES))}.")
    try:
        unit = store.update_unit(ref, {"status": status}, actor="assistant:chat")
    except StoreError as exc:
        return _reply(f"I couldn't update {ref}: {exc}", [_tool("update_unit", {"uid": ref, "status": status}, str(exc), False)])
    return _reply(
        f"**{unit['uid']}** is now `{unit['status']}`.",
        [_tool("update_unit", {"uid": unit["uid"], "status": status}, {"uid": unit["uid"]})],
        refresh=True,
    )


def _parse_dispatch(text: str):
    m = re.search(r"^(?:dispatch|hand off|派发|分配)\s+([A-Za-z]+-\d+|\d+)$", text, re.I)
    return m.group(1) if m else None


def _dispatch(store: Store, ref: str) -> dict:
    from .assistant import dispatch_prompt
    try:
        prompt = dispatch_prompt(store, ref)
    except StoreError as exc:
        return _reply(f"I couldn't dispatch {ref}: {exc}")
    return _reply(
        f"Dispatch contract for **{ref}** is ready. Copy it to the coding agent; this action does not launch a remote worker.\n\n```text\n{prompt['prompt'][:1800]}\n```",
        [_tool("dispatch_task", {"uid": ref}, {"prompt": prompt["prompt"]})],
    )


# ---------------------------------------------------------------- DSH harness
def _run_dsh(store: Store, project, text: str, status: dict,
             agent_token: str = "", base_url: str = "", infohub: dict | None = None) -> dict | None:
    command = status.get("command") or ""
    parts = shlex.split(command, posix=False)
    if not parts:
        return None
    prompt = (
        "You are the ARK project agent, working inside the local `.ark` project folder.\n"
        "You may edit files and use the ARK HTTP API to read or write nodes, links, and settings.\n"
        f"Project: {project.name} ({project.path})\n"
        f"API base: {base_url or 'http://127.0.0.1:8890'}\n"
        f"API token: {agent_token or os.environ.get('ARK_AGENT_TOKEN', '')}\n"
        + (f"InfoHub database: {_infohub_path(infohub)}\n"
           f"Current InfoHub filters: {json.dumps(_infohub_filters(infohub), ensure_ascii=False)}\n"
           if infohub else "")
        + "Prefer small, explicit changes; report what you changed.\n\n"
        f"User request:\n{text}"
    )
    try:
        proc = subprocess.run(
            parts, input=prompt, cwd=str(project.path), capture_output=True, text=True,
            timeout=int(os.environ.get("ARK_DSH_TIMEOUT", "180")), encoding="utf-8", errors="replace",
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if proc.returncode != 0:
        return None
    output = (proc.stdout or "").strip()
    if not output:
        output = (proc.stderr or "").strip()
    return {
        "message": output or "DeepSeek Harness finished without a message.",
        "tools": [],
        "refresh": True,
        "suggestions": _suggestions(),
        "harness": status["label"],
    }

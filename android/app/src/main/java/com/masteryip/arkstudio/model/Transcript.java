package com.masteryip.arkstudio.model;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Locale;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Port of the Runs thread model in {@code web/js/session-model.js}: the server
 * already merges tool results into their calls, so the client only groups
 * consecutive tools, inserts turn separators and classifies items.
 */
public final class Transcript {
    public static final List<String> ACTIVE = Arrays.asList("starting", "running", "idle", "stopping");

    private Transcript() {
    }

    /** One rendered block of the thread. */
    public static final class Block {
        public final String kind;
        public final String key;
        public final JSONObject item;
        public final List<JSONObject> items;
        public final String at;

        Block(String kind, String key, JSONObject item, List<JSONObject> items, String at) {
            this.kind = kind;
            this.key = key;
            this.item = item;
            this.items = items;
            this.at = at;
        }
    }

    public static boolean isActive(JSONObject session) {
        return session != null && ACTIVE.contains(session.optString("status", ""));
    }

    public static boolean isActiveStatus(String status) {
        return ACTIVE.contains(status);
    }

    /** running &lt; idle &lt; starting &lt; stopping &lt; errored &lt; stopped &lt; archived. */
    public static int sortRank(String status) {
        switch (status == null ? "" : status) {
            case "running": return 0;
            case "idle": return 1;
            case "starting": return 2;
            case "stopping": return 3;
            case "errored": return 4;
            case "stopped": return 5;
            case "archived": return 6;
            default: return 9;
        }
    }

    public static List<JSONObject> sortSessions(JSONArray sessions) {
        List<JSONObject> out = new ArrayList<>();
        for (int i = 0; sessions != null && i < sessions.length(); i++) {
            JSONObject s = sessions.optJSONObject(i);
            if (s != null) out.add(s);
        }
        out.sort((a, b) -> {
            int rank = sortRank(a.optString("status")) - sortRank(b.optString("status"));
            if (rank != 0) return rank;
            return b.optString("last_activity_at", "").compareTo(a.optString("last_activity_at", ""));
        });
        return out;
    }

    public static List<Block> threadBlocks(JSONArray items) {
        List<Block> blocks = new ArrayList<>();
        List<JSONObject> tools = null;
        for (int index = 0; items != null && index < items.length(); index++) {
            JSONObject item = items.optJSONObject(index);
            if (item == null) continue;
            String type = item.optString("type", "");
            String id = item.optString("id", String.valueOf(index));
            if (type.equals("tool")) {
                if (tools == null) {
                    tools = new ArrayList<>();
                    if (index == 0) separator(blocks, item);
                    blocks.add(new Block("tools", "tools-" + id, null, tools, null));
                }
                tools.add(item);
                continue;
            }
            tools = null;
            if (type.equals("message") && "user".equals(item.optString("role"))) {
                separator(blocks, item);
                blocks.add(new Block("user", id, item, null, null));
                continue;
            }
            if (index == 0) separator(blocks, item);
            String kind;
            if (type.equals("message")) kind = "assistant";
            else if (type.equals("error")) kind = "error";
            else if (type.equals("permission")) kind = "permission";
            else if (type.equals("notice") && "reasoning".equals(item.optString("level"))) kind = "reasoning";
            else kind = "notice";
            blocks.add(new Block(kind, id, item, null, null));
        }
        return blocks;
    }

    private static void separator(List<Block> blocks, JSONObject item) {
        String at = item.optString("at", "");
        if (at.isEmpty()) return;
        if (!blocks.isEmpty() && blocks.get(blocks.size() - 1).kind.equals("separator")) return;
        blocks.add(new Block("separator", "sep-" + item.optString("id"), null, null, at));
    }

    // ---------------------------------------------------------------- tools

    static JSONObject inputObject(JSONObject item) {
        Object input = item.opt("input");
        if (input instanceof JSONObject) return (JSONObject) input;
        if (input instanceof String) {
            String text = ((String) input).trim();
            if (text.startsWith("{")) {
                try {
                    return new JSONObject(text);
                } catch (JSONException plain) {
                    return new JSONObject();
                }
            }
        }
        return new JSONObject();
    }

    private static final Pattern SHELL_NAME = Pattern.compile(
            "^(shell|bash|exec|command|run|terminal|powershell|sh)$", Pattern.CASE_INSENSITIVE);

    public static String toolCommand(JSONObject item) {
        JSONObject input = inputObject(item);
        Object value = input.has("command") ? input.opt("command")
                : input.has("cmd") ? input.opt("cmd")
                : input.has("script") ? input.opt("script") : input.opt("argv");
        if (value instanceof JSONArray) {
            JSONArray array = (JSONArray) value;
            StringBuilder out = new StringBuilder();
            for (int i = 0; i < array.length(); i++) {
                if (i > 0) out.append(' ');
                out.append(array.opt(i));
            }
            return out.toString();
        }
        if (value instanceof String) return (String) value;
        Object raw = item.opt("input");
        if (raw instanceof String && SHELL_NAME.matcher(item.optString("name", "")).matches()) return (String) raw;
        return "";
    }

    private static final Pattern EDIT = Pattern.compile(
            "(apply_?patch|edit|write|patch|str_replace|create_file|multiedit|notebookedit|file_change)");
    private static final Pattern SEARCH = Pattern.compile(
            "(grep|search|glob|find|ripgrep|^rg$|^ls$|list_dir|web_search|websearch)");
    private static final Pattern READ = Pattern.compile("(read|view|^cat$|open_file|fetch|webfetch)");
    private static final Pattern COMMAND = Pattern.compile(
            "(shell|bash|exec|command|^run|terminal|powershell|^sh$|local_shell)");

    public static String toolKind(JSONObject item) {
        String name = item.optString("name", "").toLowerCase(Locale.ROOT);
        if (EDIT.matcher(name).find()) return "edit";
        if (SEARCH.matcher(name).find()) return "search";
        if (READ.matcher(name).find()) return "read";
        if (COMMAND.matcher(name).find() || !toolCommand(item).isEmpty()) return "command";
        return "other";
    }

    private static final Pattern PATCH_FILE = Pattern.compile(
            "^\\*\\*\\* (?:Update|Add|Delete) File: (.+)$", Pattern.MULTILINE);

    public static List<String> toolPaths(JSONObject item) {
        JSONObject input = inputObject(item);
        LinkedHashSet<String> out = new LinkedHashSet<>();
        for (String key : new String[]{"path", "file_path", "filePath", "file", "target_file"}) {
            Object value = input.opt(key);
            if (value instanceof String && !((String) value).isEmpty()) out.add((String) value);
        }
        String patch = input.optString("patch", input.optString("input", ""));
        if (patch.isEmpty() && item.opt("input") instanceof String) patch = item.optString("input");
        Matcher m = PATCH_FILE.matcher(patch);
        while (m.find()) out.add(m.group(1).trim());
        return new ArrayList<>(out);
    }

    /** One-line title for a collapsed tool row, e.g. {@code $ git status} or a file path. */
    public static String toolTitle(JSONObject item) {
        String command = toolCommand(item);
        if (!command.isEmpty()) return "$ " + firstLine(command);
        List<String> paths = toolPaths(item);
        String name = item.optString("name", "tool");
        if (!paths.isEmpty()) return name + " " + paths.get(0);
        JSONObject input = inputObject(item);
        for (String key : new String[]{"pattern", "query", "url", "description"}) {
            String value = input.optString(key, "");
            if (!value.isEmpty()) return name + " " + firstLine(value);
        }
        return name;
    }

    /** Group header such as "2 commands · 1 search · 1 running". */
    public static String toolSummary(List<JSONObject> tools) {
        int command = 0, edit = 0, search = 0, read = 0, other = 0, running = 0, failed = 0;
        for (JSONObject tool : tools) {
            switch (toolKind(tool)) {
                case "command": command++; break;
                case "edit": edit++; break;
                case "search": search++; break;
                case "read": read++; break;
                default: other++;
            }
            if ("running".equals(tool.optString("status"))) running++;
            if ("error".equals(tool.optString("status"))) failed++;
        }
        List<String> parts = new ArrayList<>();
        if (edit > 0) parts.add(edit + (edit == 1 ? " edit" : " edits"));
        if (command > 0) parts.add(command + (command == 1 ? " command" : " commands"));
        if (search > 0) parts.add(search + (search == 1 ? " search" : " searches"));
        if (read > 0) parts.add(read + (read == 1 ? " read" : " reads"));
        if (other > 0) parts.add(other + (other == 1 ? " tool" : " tools"));
        if (running > 0) parts.add(running + " running");
        if (failed > 0) parts.add(failed + " failed");
        return String.join(" · ", parts);
    }

    /** Tool output as display text (strings verbatim, JSON pretty-ish). */
    public static String outputText(JSONObject item) {
        Object output = item.opt("output");
        if (output == null || output == JSONObject.NULL) return "";
        if (output instanceof String) return (String) output;
        try {
            if (output instanceof JSONObject) return ((JSONObject) output).toString(2);
            if (output instanceof JSONArray) return ((JSONArray) output).toString(2);
        } catch (JSONException ignored) {
            // fall through
        }
        return String.valueOf(output);
    }

    private static String firstLine(String text) {
        String trimmed = text.trim();
        int nl = trimmed.indexOf('\n');
        return nl < 0 ? trimmed : trimmed.substring(0, nl) + " …";
    }

    /** "steer" while the run is active (POST …/steer), otherwise "continue" (POST …/continue). */
    public static String composerMode(JSONObject session) {
        if (session == null) return "none";
        return isActive(session) ? "steer" : "continue";
    }
}

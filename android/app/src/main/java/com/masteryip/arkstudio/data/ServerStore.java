package com.masteryip.arkstudio.data;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Locale;
import java.util.Map;

/**
 * Saved-server list with JSON persistence and de-duplication.
 *
 * <p>This class is pure Java on purpose: {@code MainActivity} uses it with
 * SharedPreferences, while JVM unit tests exercise the same normalization,
 * de-duplication, selection and serialization rules without an emulator.</p>
 */
public final class ServerStore {
    private static final int FORMAT_VERSION = 1;
    private static final int MAX_JSON_LENGTH = 1_000_000;

    private final List<ServerEntry> servers = new ArrayList<>();
    private String lastSelectedUrl = "";

    public static ServerStore fromJson(String json) {
        ServerStore store = new ServerStore();
        if (json == null || json.trim().isEmpty() || json.length() > MAX_JSON_LENGTH) {
            return store;
        }
        try {
            Object rootValue = new JsonParser(json).parse();
            if (!(rootValue instanceof Map)) {
                return store;
            }
            Map<?, ?> root = (Map<?, ?>) rootValue;

            Object savedServers = root.get("servers");
            if (savedServers instanceof List) {
                for (Object item : (List<?>) savedServers) {
                    if (!(item instanceof Map)) {
                        continue;
                    }
                    Map<?, ?> object = (Map<?, ?>) item;
                    String url = asString(object.get("url"));
                    String normalized = ServerUrl.normalize(url);
                    if (normalized == null) {
                        continue;
                    }
                    String name = asString(object.get("name"));
                    long lastUsedAt = asLong(object.get("lastUsedAt"));
                    String lastError = asString(object.get("lastError"));
                    ServerEntry entry = new ServerEntry(name, normalized, lastUsedAt, lastError);

                    int existing = store.indexOf(entry.getUrl());
                    if (existing >= 0) {
                        store.servers.set(existing, entry);
                    } else {
                        store.servers.add(entry);
                    }
                }
            }

            String selected = asString(root.get("lastSelectedUrl"));
            if (selected != null) {
                ServerEntry match = store.get(selected);
                if (match != null) {
                    store.lastSelectedUrl = match.getUrl();
                }
            }
        } catch (RuntimeException ignored) {
            // Corrupt or externally edited preferences should not break startup.
            return new ServerStore();
        }
        return store;
    }

    public String toJson() {
        StringBuilder out = new StringBuilder(256);
        out.append("{\"version\":").append(FORMAT_VERSION);
        out.append(",\"lastSelectedUrl\":");
        writeString(out, lastSelectedUrl);
        out.append(",\"servers\":[");
        for (int i = 0; i < servers.size(); i++) {
            if (i > 0) {
                out.append(',');
            }
            ServerEntry entry = servers.get(i);
            out.append("{\"name\":");
            writeString(out, entry.getName());
            out.append(",\"url\":");
            writeString(out, entry.getUrl());
            out.append(",\"lastUsedAt\":").append(entry.getLastUsedAt());
            out.append(",\"lastError\":");
            writeString(out, entry.getLastError());
            out.append('}');
        }
        out.append("]}");
        return out.toString();
    }

    public List<ServerEntry> entries() {
        return Collections.unmodifiableList(new ArrayList<>(servers));
    }

    public int size() {
        return servers.size();
    }

    public boolean isEmpty() {
        return servers.isEmpty();
    }

    public String getLastSelectedUrl() {
        return lastSelectedUrl;
    }

    public ServerEntry getLastSelected() {
        return get(lastSelectedUrl);
    }

    public boolean isSelected(ServerEntry entry) {
        return entry != null
                && !lastSelectedUrl.isEmpty()
                && ServerUrl.sameServer(lastSelectedUrl, entry.getUrl());
    }

    public ServerEntry get(String rawUrl) {
        int index = indexOf(rawUrl);
        return index < 0 ? null : servers.get(index);
    }

    /**
     * Adds a server or refreshes the entry with the same normalized identity.
     * A non-empty name replaces the saved name; an empty name keeps it. The most
     * recently supplied URL wins (so a fresh QR token can replace an old one).
     */
    public AddResult addOrUpdate(String name, String rawUrl) {
        String normalized = ServerUrl.normalize(rawUrl);
        if (normalized == null) {
            return null;
        }
        String cleanName = name == null ? "" : name.trim();
        int index = indexOf(normalized);
        if (index >= 0) {
            ServerEntry existing = servers.get(index);
            ServerEntry updated = new ServerEntry(
                    cleanName.isEmpty() ? existing.getName() : cleanName,
                    normalized,
                    existing.getLastUsedAt(),
                    existing.getLastError());
            servers.set(index, updated);
            return new AddResult(updated, false);
        }
        ServerEntry entry = new ServerEntry(cleanName, normalized, 0L, null);
        servers.add(entry);
        return new AddResult(entry, true);
    }

    /** Marks a server as selected and records a successful/attempted connection. */
    public ServerEntry touch(String rawUrl) {
        int index = indexOf(rawUrl);
        if (index < 0) {
            return null;
        }
        ServerEntry updated = servers.get(index).withLastUsedAt(System.currentTimeMillis()).withoutError();
        servers.set(index, updated);
        lastSelectedUrl = updated.getUrl();
        return updated;
    }

    public ServerEntry touch(String rawUrl, long timestamp) {
        int index = indexOf(rawUrl);
        if (index < 0) {
            return null;
        }
        ServerEntry updated = servers.get(index).withLastUsedAt(timestamp).withoutError();
        servers.set(index, updated);
        lastSelectedUrl = updated.getUrl();
        return updated;
    }

    public boolean select(String rawUrl) {
        int index = indexOf(rawUrl);
        if (index < 0) {
            return false;
        }
        lastSelectedUrl = servers.get(index).getUrl();
        return true;
    }

    /** Keeps the entry but records an unreachable/error state. */
    public ServerEntry markError(String rawUrl, String message) {
        int index = indexOf(rawUrl);
        if (index < 0) {
            return null;
        }
        String detail = message == null || message.trim().isEmpty() ? "Could not connect" : message.trim();
        ServerEntry updated = servers.get(index).withError(detail);
        servers.set(index, updated);
        return updated;
    }

    public boolean clearError(String rawUrl) {
        int index = indexOf(rawUrl);
        if (index < 0 || servers.get(index).getLastError() == null) {
            return false;
        }
        servers.set(index, servers.get(index).withoutError());
        return true;
    }

    /**
     * Removes exactly one entry. If it was selected, the most recently used
     * remaining server becomes selected so the home screen still has a sensible
     * active state.
     */
    public boolean remove(String rawUrl) {
        int index = indexOf(rawUrl);
        if (index < 0) {
            return false;
        }
        ServerEntry removed = servers.remove(index);
        if (!lastSelectedUrl.isEmpty() && ServerUrl.sameServer(lastSelectedUrl, removed.getUrl())) {
            ServerEntry fallback = null;
            for (ServerEntry candidate : servers) {
                if (fallback == null || candidate.getLastUsedAt() > fallback.getLastUsedAt()) {
                    fallback = candidate;
                }
            }
            lastSelectedUrl = fallback == null ? "" : fallback.getUrl();
        }
        return true;
    }

    private int indexOf(String rawUrl) {
        String identity = ServerUrl.identity(rawUrl);
        if (identity == null) {
            return -1;
        }
        for (int i = 0; i < servers.size(); i++) {
            if (identity.equals(ServerUrl.identity(servers.get(i).getUrl()))) {
                return i;
            }
        }
        return -1;
    }

    private static String asString(Object value) {
        return value instanceof String ? (String) value : null;
    }

    private static long asLong(Object value) {
        if (value instanceof Number) {
            return ((Number) value).longValue();
        }
        if (value instanceof String) {
            try {
                return Long.parseLong((String) value);
            } catch (NumberFormatException ignored) {
                return 0L;
            }
        }
        return 0L;
    }

    private static void writeString(StringBuilder out, String value) {
        out.append('"');
        if (value != null) {
            for (int i = 0; i < value.length(); i++) {
                char c = value.charAt(i);
                switch (c) {
                    case '"':
                        out.append("\\\"");
                        break;
                    case '\\':
                        out.append("\\\\");
                        break;
                    case '\b':
                        out.append("\\b");
                        break;
                    case '\f':
                        out.append("\\f");
                        break;
                    case '\n':
                        out.append("\\n");
                        break;
                    case '\r':
                        out.append("\\r");
                        break;
                    case '\t':
                        out.append("\\t");
                        break;
                    default:
                        if (c < 0x20) {
                            out.append(String.format(Locale.ROOT, "\\u%04x", (int) c));
                        } else {
                            out.append(c);
                        }
                }
            }
        }
        out.append('"');
    }

    public static final class AddResult {
        private final ServerEntry entry;
        private final boolean added;

        AddResult(ServerEntry entry, boolean added) {
            this.entry = entry;
            this.added = added;
        }

        public ServerEntry getEntry() {
            return entry;
        }

        public boolean isAdded() {
            return added;
        }
    }

    /**
     * Tiny dependency-free JSON reader. The prefs file is local and written by
     * this class, but robust parsing keeps startup safe after manual edits.
     */
    private static final class JsonParser {
        private final String text;
        private int position;

        JsonParser(String text) {
            this.text = text;
        }

        Object parse() {
            skipWhitespace();
            Object value = readValue();
            skipWhitespace();
            if (position != text.length()) {
                throw new IllegalArgumentException("Trailing JSON content");
            }
            return value;
        }

        private Object readValue() {
            if (position >= text.length()) {
                throw new IllegalArgumentException("Unexpected end of JSON");
            }
            char c = text.charAt(position);
            switch (c) {
                case '{':
                    return readObject();
                case '[':
                    return readArray();
                case '"':
                    return readString();
                case 't':
                    expect("true");
                    return Boolean.TRUE;
                case 'f':
                    expect("false");
                    return Boolean.FALSE;
                case 'n':
                    expect("null");
                    return null;
                default:
                    if (c == '-' || (c >= '0' && c <= '9')) {
                        return readNumber();
                    }
                    throw new IllegalArgumentException("Unexpected JSON token");
            }
        }

        private Map<String, Object> readObject() {
            Map<String, Object> object = new java.util.LinkedHashMap<>();
            position++; // {
            skipWhitespace();
            if (peek('}')) {
                position++;
                return object;
            }
            while (true) {
                skipWhitespace();
                String key = readString();
                skipWhitespace();
                expect(':');
                skipWhitespace();
                object.put(key, readValue());
                skipWhitespace();
                if (peek('}')) {
                    position++;
                    return object;
                }
                expect(',');
            }
        }

        private List<Object> readArray() {
            List<Object> array = new ArrayList<>();
            position++; // [
            skipWhitespace();
            if (peek(']')) {
                position++;
                return array;
            }
            while (true) {
                skipWhitespace();
                array.add(readValue());
                skipWhitespace();
                if (peek(']')) {
                    position++;
                    return array;
                }
                expect(',');
            }
        }

        private String readString() {
            expect('"');
            StringBuilder out = new StringBuilder();
            while (position < text.length()) {
                char c = text.charAt(position++);
                if (c == '"') {
                    return out.toString();
                }
                if (c != '\\') {
                    if (c < 0x20) {
                        throw new IllegalArgumentException("Control character in JSON string");
                    }
                    out.append(c);
                    continue;
                }
                if (position >= text.length()) {
                    throw new IllegalArgumentException("Bad JSON escape");
                }
                char escaped = text.charAt(position++);
                switch (escaped) {
                    case '"':
                        out.append('"');
                        break;
                    case '\\':
                        out.append('\\');
                        break;
                    case '/':
                        out.append('/');
                        break;
                    case 'b':
                        out.append('\b');
                        break;
                    case 'f':
                        out.append('\f');
                        break;
                    case 'n':
                        out.append('\n');
                        break;
                    case 'r':
                        out.append('\r');
                        break;
                    case 't':
                        out.append('\t');
                        break;
                    case 'u':
                        if (position + 4 > text.length()) {
                            throw new IllegalArgumentException("Bad JSON unicode escape");
                        }
                        String hex = text.substring(position, position + 4);
                        position += 4;
                        try {
                            out.append((char) Integer.parseInt(hex, 16));
                        } catch (NumberFormatException failed) {
                            throw new IllegalArgumentException("Bad JSON unicode escape");
                        }
                        break;
                    default:
                        throw new IllegalArgumentException("Bad JSON escape");
                }
            }
            throw new IllegalArgumentException("Unterminated JSON string");
        }

        private Object readNumber() {
            int start = position;
            if (peek('-')) {
                position++;
            }
            while (position < text.length() && Character.isDigit(text.charAt(position))) {
                position++;
            }
            boolean floating = false;
            if (peek('.')) {
                floating = true;
                position++;
                while (position < text.length() && Character.isDigit(text.charAt(position))) {
                    position++;
                }
            }
            if (peek('e') || peek('E')) {
                floating = true;
                position++;
                if (peek('+') || peek('-')) {
                    position++;
                }
                while (position < text.length() && Character.isDigit(text.charAt(position))) {
                    position++;
                }
            }
            String raw = text.substring(start, position);
            try {
                return floating ? Double.valueOf(raw) : Long.valueOf(raw);
            } catch (NumberFormatException failed) {
                throw new IllegalArgumentException("Bad JSON number");
            }
        }

        private void expect(String token) {
            if (!text.startsWith(token, position)) {
                throw new IllegalArgumentException("Expected " + token);
            }
            position += token.length();
        }

        private void expect(char expected) {
            if (position >= text.length() || text.charAt(position) != expected) {
                throw new IllegalArgumentException("Expected " + expected);
            }
            position++;
        }

        private boolean peek(char expected) {
            return position < text.length() && text.charAt(position) == expected;
        }

        private void skipWhitespace() {
            while (position < text.length()) {
                char c = text.charAt(position);
                if (c == ' ' || c == '\n' || c == '\r' || c == '\t') {
                    position++;
                } else {
                    break;
                }
            }
        }
    }
}

package com.masteryip.arkstudio.data;

import java.util.Objects;

/** Immutable saved ARK server. Pure Java so it can be unit-tested on the JVM. */
public final class ServerEntry {
    private final String name;
    private final String url;
    private final long lastUsedAt;
    private final String lastError;

    public ServerEntry(String name, String url, long lastUsedAt, String lastError) {
        String normalized = ServerUrl.normalize(url);
        if (normalized == null) {
            throw new IllegalArgumentException("Invalid ARK server URL: " + url);
        }
        this.url = normalized;

        String cleanName = name == null ? "" : name.trim();
        this.name = cleanName.isEmpty() ? ServerUrl.defaultName(normalized) : cleanName;
        this.lastUsedAt = Math.max(0L, lastUsedAt);

        String cleanError = lastError == null ? "" : lastError.trim();
        this.lastError = cleanError.isEmpty() ? null : cleanError;
    }

    public String getName() {
        return name;
    }

    public String getUrl() {
        return url;
    }

    public long getLastUsedAt() {
        return lastUsedAt;
    }

    public String getLastError() {
        return lastError;
    }

    public ServerEntry withName(String newName) {
        return new ServerEntry(newName, url, lastUsedAt, lastError);
    }

    public ServerEntry withUrl(String newUrl) {
        return new ServerEntry(name, newUrl, lastUsedAt, lastError);
    }

    public ServerEntry withLastUsedAt(long timestamp) {
        return new ServerEntry(name, url, timestamp, lastError);
    }

    public ServerEntry withError(String error) {
        return new ServerEntry(name, url, lastUsedAt, error);
    }

    public ServerEntry withoutError() {
        return new ServerEntry(name, url, lastUsedAt, null);
    }

    @Override
    public boolean equals(Object other) {
        if (this == other) {
            return true;
        }
        if (!(other instanceof ServerEntry)) {
            return false;
        }
        ServerEntry that = (ServerEntry) other;
        return lastUsedAt == that.lastUsedAt
                && name.equals(that.name)
                && url.equals(that.url)
                && Objects.equals(lastError, that.lastError);
    }

    @Override
    public int hashCode() {
        return Objects.hash(name, url, lastUsedAt, lastError);
    }

    @Override
    public String toString() {
        return "ServerEntry{name='" + name + "', url='" + url + "', lastUsedAt=" + lastUsedAt
                + ", lastError='" + lastError + "'}";
    }
}

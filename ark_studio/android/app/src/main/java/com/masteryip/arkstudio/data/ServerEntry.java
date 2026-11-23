package com.masteryip.arkstudio.data;

import java.util.Objects;

/**
 * Immutable saved ARK server. Pure Java so it can be unit-tested on the JVM.
 *
 * <p>{@code serverId} and {@code publicKey} are the server identity stored at
 * pairing ({@link ServerIdentity}); {@code tunnel} marks a server reached
 * through a Cloudflare Tunnel. All three are optional: entries saved by older
 * versions have none of them.</p>
 */
public final class ServerEntry {
    private final String name;
    private final String url;
    private final long lastUsedAt;
    private final String lastError;
    private final String serverId;
    private final String publicKey;
    private final boolean tunnel;

    public ServerEntry(String name, String url, long lastUsedAt, String lastError) {
        this(name, url, lastUsedAt, lastError, null, null, false);
    }

    public ServerEntry(String name, String url, long lastUsedAt, String lastError,
                       String serverId, String publicKey, boolean tunnel) {
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

        boolean identity = ServerIdentity.validServerId(serverId) && publicKey != null && !publicKey.trim().isEmpty();
        this.serverId = identity ? serverId : null;
        this.publicKey = identity ? publicKey.trim() : null;
        this.tunnel = tunnel;
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

    /** The stored server id, or {@code null} before the identity was fetched. */
    public String getServerId() {
        return serverId;
    }

    public String getPublicKey() {
        return publicKey;
    }

    public boolean hasIdentity() {
        return serverId != null;
    }

    /** Paired through a Cloudflare Tunnel, or a quick-tunnel address. */
    public boolean isTunnel() {
        return tunnel || PairingLink.isQuickTunnel(url);
    }

    /** Whether the address can change (a quick tunnel) and re-link applies. */
    public boolean canRelink() {
        return serverId != null && isTunnel();
    }

    public ServerEntry withName(String newName) {
        return new ServerEntry(newName, url, lastUsedAt, lastError, serverId, publicKey, tunnel);
    }

    public ServerEntry withUrl(String newUrl) {
        return new ServerEntry(name, newUrl, lastUsedAt, lastError, serverId, publicKey, tunnel);
    }

    public ServerEntry withLastUsedAt(long timestamp) {
        return new ServerEntry(name, url, timestamp, lastError, serverId, publicKey, tunnel);
    }

    public ServerEntry withError(String error) {
        return new ServerEntry(name, url, lastUsedAt, error, serverId, publicKey, tunnel);
    }

    public ServerEntry withoutError() {
        return new ServerEntry(name, url, lastUsedAt, null, serverId, publicKey, tunnel);
    }

    public ServerEntry withIdentity(String id, String key, boolean viaTunnel) {
        return new ServerEntry(name, url, lastUsedAt, lastError, id, key, tunnel || viaTunnel);
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
                && tunnel == that.tunnel
                && name.equals(that.name)
                && url.equals(that.url)
                && Objects.equals(lastError, that.lastError)
                && Objects.equals(serverId, that.serverId)
                && Objects.equals(publicKey, that.publicKey);
    }

    @Override
    public int hashCode() {
        return Objects.hash(name, url, lastUsedAt, lastError, serverId, publicKey, tunnel);
    }

    @Override
    public String toString() {
        return "ServerEntry{name='" + name + "', url='" + url + "', lastUsedAt=" + lastUsedAt
                + ", lastError='" + lastError + "', serverId='" + serverId + "', tunnel=" + tunnel + "}";
    }
}

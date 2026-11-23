package com.masteryip.arkstudio.data;

import java.nio.charset.StandardCharsets;
import java.security.KeyFactory;
import java.security.MessageDigest;
import java.security.PublicKey;
import java.security.SecureRandom;
import java.security.Signature;
import java.security.interfaces.ECPublicKey;
import java.security.spec.X509EncodedKeySpec;
import java.util.Base64;
import java.util.Locale;
import java.util.regex.Pattern;

/**
 * The server identity of {@code GET /api/mobile/identity?nonce=…}: a P-256
 * key per ARK server ({@code ark_studio/server/internal/mobile/identity.go}).
 *
 * <p>The phone stores the server id and public key when it pairs. A Cloudflare
 * quick tunnel gets a new address after every restart; before the phone moves
 * its token to a new address, that address must sign a fresh nonce with the
 * stored key. Pure Java (JCA only) so the checks run in JVM tests.</p>
 */
public final class ServerIdentity {
    public static final String CONTEXT = "ark-server-identity/1";
    public static final String ALGORITHM = "ECDSA-P256-SHA256";
    private static final Pattern NONCE = Pattern.compile("^[A-Za-z0-9_-]{16,128}$");
    private static final Pattern SERVER_ID = Pattern.compile("^[0-9a-f]{20}$");
    private static final SecureRandom RANDOM = new SecureRandom();

    /** Why an identity answer was refused. */
    public enum Problem {
        NONE, MALFORMED, WRONG_HOST, WRONG_NONCE, WRONG_KEY, WRONG_SERVER, BAD_SIGNATURE
    }

    public final String serverId;
    public final String publicKey;

    private ServerIdentity(String serverId, String publicKey) {
        this.serverId = serverId;
        this.publicKey = publicKey;
    }

    /** A random 32-character URL-safe nonce. */
    public static String newNonce() {
        byte[] bytes = new byte[24];
        RANDOM.nextBytes(bytes);
        return Base64.getUrlEncoder().withoutPadding().encodeToString(bytes);
    }

    public static boolean validNonce(String nonce) {
        return nonce != null && NONCE.matcher(nonce).matches();
    }

    public static boolean validServerId(String id) {
        return id != null && SERVER_ID.matcher(id).matches();
    }

    /** UTF-8 of context, server id, host and nonce, one per line. */
    public static byte[] message(String serverId, String host, String nonce) {
        return (CONTEXT + "\n" + serverId + "\n" + host + "\n" + nonce).getBytes(StandardCharsets.UTF_8);
    }

    /** First 20 lowercase hex digits of SHA-256 of the public key's DER. */
    public static String serverIdFor(byte[] publicKeyDer) {
        try {
            byte[] sum = MessageDigest.getInstance("SHA-256").digest(publicKeyDer);
            StringBuilder hex = new StringBuilder(40);
            for (byte b : sum) hex.append(String.format(Locale.ROOT, "%02x", b & 0xff));
            return hex.substring(0, 20);
        } catch (Exception impossible) {
            throw new IllegalStateException(impossible);
        }
    }

    /** True when {@code signature} is a valid ECDSA-SHA256 signature by {@code publicKey}. */
    public static boolean verify(String publicKey, String serverId, String host, String nonce, String signature) {
        try {
            byte[] der = Base64.getDecoder().decode(publicKey);
            if (!serverIdFor(der).equals(serverId)) return false;
            PublicKey key = KeyFactory.getInstance("EC").generatePublic(new X509EncodedKeySpec(der));
            if (!(key instanceof ECPublicKey)
                    || ((ECPublicKey) key).getParams().getCurve().getField().getFieldSize() != 256) {
                return false;
            }
            Signature check = Signature.getInstance("SHA256withECDSA");
            check.initVerify(key);
            check.update(message(serverId, host, nonce));
            return check.verify(Base64.getDecoder().decode(signature));
        } catch (Exception invalid) {
            return false;
        }
    }

    /** Lowercase host of a URL ({@code ""} when there is none). */
    public static String hostOf(String url) {
        try {
            String host = new java.net.URI(url.trim()).getHost();
            if (host == null) return "";
            host = host.toLowerCase(Locale.ROOT);
            if (host.startsWith("[") && host.endsWith("]")) host = host.substring(1, host.length() - 1);
            return host;
        } catch (Exception failed) {
            return "";
        }
    }

    /** Result of {@link #check}: the identity, or why it was refused. */
    public static final class Check {
        public final ServerIdentity identity;
        public final Problem problem;
        /** The server said the request came through its Cloudflare Tunnel. */
        public final boolean tunnel;

        Check(ServerIdentity identity, Problem problem) {
            this(identity, problem, false);
        }

        Check(ServerIdentity identity, Problem problem, boolean tunnel) {
            this.identity = identity;
            this.problem = problem;
            this.tunnel = tunnel;
        }

        public boolean ok() {
            return problem == Problem.NONE;
        }
    }

    /**
     * Checks an identity answer the phone asked for at {@code baseUrl} with
     * {@code nonce}. Without {@code storedKey} the answer only has to be
     * consistent (first contact, right after pairing); with it, the server must
     * hold the stored key and have the stored id. The other arguments are the
     * answer's server_id, public_key, host, nonce and signature.
     */
    public static Check check(String baseUrl, String nonce, String serverId, String publicKey, String host,
                              String echoedNonce, String signature, String storedId, String storedKey) {
        if (!validServerId(serverId) || isBlank(publicKey) || isBlank(signature) || !validNonce(nonce)) {
            return new Check(null, Problem.MALFORMED);
        }
        String expectedHost = hostOf(baseUrl);
        if (expectedHost.isEmpty() || !expectedHost.equals(host == null ? "" : host.toLowerCase(Locale.ROOT))) {
            return new Check(null, Problem.WRONG_HOST);
        }
        if (!nonce.equals(echoedNonce)) return new Check(null, Problem.WRONG_NONCE);
        if (storedId != null && !storedId.isEmpty() && !storedId.equals(serverId)) {
            return new Check(null, Problem.WRONG_SERVER);
        }
        if (storedKey != null && !storedKey.isEmpty() && !sameKey(storedKey, publicKey)) {
            return new Check(null, Problem.WRONG_KEY);
        }
        String key = storedKey != null && !storedKey.isEmpty() ? storedKey : publicKey;
        if (!verify(key, serverId, expectedHost, nonce, signature)) return new Check(null, Problem.BAD_SIGNATURE);
        return new Check(new ServerIdentity(serverId, key), Problem.NONE);
    }

    /** {@link #check} on the JSON answer of {@code GET /api/mobile/identity}. */
    public static Check check(String baseUrl, String nonce, org.json.JSONObject answer,
                              String storedId, String storedKey) {
        if (answer == null) return new Check(null, Problem.MALFORMED);
        Check result = check(baseUrl, nonce, answer.optString("server_id", ""), answer.optString("public_key", ""),
                answer.optString("host", ""), answer.optString("nonce", ""), answer.optString("signature", ""),
                storedId, storedKey);
        return new Check(result.identity, result.problem, answer.optBoolean("tunnel", false));
    }

    private static boolean sameKey(String a, String b) {
        try {
            return MessageDigest.isEqual(Base64.getDecoder().decode(a), Base64.getDecoder().decode(b));
        } catch (IllegalArgumentException invalid) {
            return false;
        }
    }

    private static boolean isBlank(String s) {
        return s == null || s.trim().isEmpty();
    }
}

package com.masteryip.arkstudio;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.Context;
import android.graphics.Typeface;
import android.os.SystemClock;
import android.text.InputType;
import android.view.Gravity;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.TextView;
import android.widget.Toast;

import com.masteryip.arkstudio.api.ApiException;
import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.data.PairingLink;
import com.masteryip.arkstudio.data.ServerEntry;
import com.masteryip.arkstudio.data.ServerIdentity;
import com.masteryip.arkstudio.data.ServerStore;
import com.masteryip.arkstudio.data.ServerUrl;
import com.masteryip.arkstudio.platform.Accounts;
import com.masteryip.arkstudio.ui.Async;
import com.masteryip.arkstudio.ui.Ui;

import org.json.JSONObject;

import java.util.Locale;

/**
 * Pairing and re-linking through a Cloudflare Tunnel
 * ({@code ark_studio/server/internal/server/mobile_tunnel.go}).
 *
 * <ul>
 *   <li>A pairing code claimed through the tunnel waits for the computer: the
 *   phone shows the 6-digit confirm code and polls
 *   {@code POST /api/mobile/pair/claim/status} until the computer confirms or
 *   declines.</li>
 *   <li>After pairing the phone stores the server id and public key
 *   ({@code GET /api/mobile/identity}).</li>
 *   <li>Re-link: when a quick tunnel restarted under a new address, the new
 *   address must sign a fresh nonce with the stored key before the saved
 *   server, its token and cookie move there.</li>
 * </ul>
 */
final class TunnelFlows {
    /** What the server list provides. */
    interface Host {
        /** A claim result with {@code mobile_token}: store it as any pairing. */
        void paired(String base, JSONObject response);

        /** Opens the QR scanner; a plain URL scanned there re-links {@code fromUrl}. */
        void scanForRelink(String fromUrl);

        /** The list changed (re-link done). */
        void changed(String newUrl);
    }

    private final Activity a;
    private final Host host;

    TunnelFlows(Activity activity, Host host) {
        this.a = activity;
        this.host = host;
    }

    // ------------------------------------------------------------ confirm

    /**
     * Shows the confirm code of a pending claim and polls until the computer
     * answers. Cancel stops polling; the server lets the request expire.
     */
    void awaitConfirmation(String base, String code, JSONObject pending) {
        String claimId = pending.optString("claim_id", "");
        String secret = pending.optString("claim_secret", "");
        long deadline = SystemClock.elapsedRealtime() + Math.max(10, pending.optLong("expires_in", 180)) * 1000L;
        long poll = Math.min(10, Math.max(1, pending.optLong("poll_after", 2))) * 1000L;

        LinearLayout box = Ui.column(a);
        box.setPadding(Ui.dp(a, 22), Ui.dp(a, 8), Ui.dp(a, 22), Ui.dp(a, 4));
        TextView digits = Ui.label(a, spaced(pending.optString("confirm_code", "")), 40, Ui.accent(a), true);
        digits.setTypeface(Typeface.MONOSPACE, Typeface.BOLD);
        digits.setGravity(Gravity.CENTER);
        digits.setLetterSpacing(0.08f);
        digits.setPadding(0, Ui.dp(a, 8), 0, Ui.dp(a, 12));
        digits.setTextIsSelectable(false);
        box.addView(digits, Ui.lp(Ui.MATCH, Ui.WRAP));
        box.addView(Ui.body(a, a.getString(R.string.confirm_body)));
        TextView status = Ui.small(a, "");
        status.setPadding(0, Ui.dp(a, 12), 0, 0);
        box.addView(status);
        TextView where = Ui.small(a, ServerUrl.displayHost(base));
        where.setTextColor(Ui.faint(a));
        box.addView(where);

        AlertDialog dialog = new AlertDialog.Builder(a)
                .setTitle(R.string.confirm_title)
                .setView(box)
                .setCancelable(false)
                .setNegativeButton(R.string.cancel, (d, which) -> {
                    // Withdraw the request so it leaves the computer's list.
                    Async.run(a, () -> ArkClient.cancelClaim(base, claimId, secret), ignored -> { }, ignored -> { });
                })
                .create();
        boolean[] stopped = {false};
        dialog.setOnDismissListener(d -> stopped[0] = true);
        Runnable[] tick = new Runnable[1];
        tick[0] = () -> {
            if (stopped[0] || a.isFinishing()) return;
            long left = deadline - SystemClock.elapsedRealtime();
            if (left <= 0) {
                dialog.dismiss();
                failure(a.getString(R.string.pair_confirm_expired));
                return;
            }
            Async.run(a, () -> ArkClient.claimStatus(base, code, claimId, secret), response -> {
                if (stopped[0]) return;
                if (ArkClient.isPending(response)) {
                    status.setText(a.getString(R.string.confirm_waiting, clock(deadline)));
                    Async.main().postDelayed(tick[0], poll);
                    return;
                }
                dialog.dismiss();
                host.paired(base, response);
            }, error -> {
                if (stopped[0]) return;
                if (error.isNetwork() || error.getStatus() >= 500) {
                    status.setText(a.getString(R.string.confirm_retrying, clock(deadline)));
                    Async.main().postDelayed(tick[0], poll);
                    return;
                }
                dialog.dismiss();
                failure("PAIRING_REJECTED".equals(error.getCode()) ? a.getString(R.string.pair_rejected)
                        : "PAIRING_CONFIRM_EXPIRED".equals(error.getCode())
                        ? a.getString(R.string.pair_confirm_expired) : error.getMessage());
            });
        };
        status.setText(a.getString(R.string.confirm_waiting, clock(deadline)));
        dialog.show();
        Async.main().postDelayed(tick[0], poll);
    }

    private void failure(String message) {
        new AlertDialog.Builder(a)
                .setTitle(R.string.pair_failed)
                .setMessage(message)
                .setPositiveButton(android.R.string.ok, null)
                .show();
    }

    /** {@code 123456} → {@code 123 456}. */
    static String spaced(String code) {
        return code.length() == 6 ? code.substring(0, 3) + " " + code.substring(3) : code;
    }

    private static String clock(long deadline) {
        long s = Math.max(0, (deadline - SystemClock.elapsedRealtime()) / 1000);
        return String.format(Locale.ROOT, "%d:%02d", s / 60, s % 60);
    }

    // ----------------------------------------------------------- identity

    /**
     * Fetches and checks the server identity (consistency only: first
     * contact) and stores it with the saved server. Old servers without the
     * endpoint and failures are ignored. Runs on a worker thread.
     */
    static boolean fetchIdentity(Context c, String base) {
        try {
            String nonce = ServerIdentity.newNonce();
            JSONObject answer = ArkClient.identity(base, nonce);
            ServerIdentity.Check check = ServerIdentity.check(base, nonce, answer, null, null);
            if (!check.ok()) return false;
            return Accounts.storeIdentity(c, base, check.identity,
                    check.tunnel || PairingLink.isQuickTunnel(base)) != null;
        } catch (ApiException failed) {
            return false;
        }
    }

    // ------------------------------------------------------------- re-link

    /** The re-link dialog: short code or address, or Scan QR. */
    void showRelink(String fromUrl, String prefill, String error) {
        LinearLayout box = Ui.column(a);
        box.setPadding(Ui.dp(a, 20), Ui.dp(a, 8), Ui.dp(a, 20), 0);
        ServerEntry from = fromUrl == null ? null : Accounts.servers(a).get(fromUrl);
        if (from != null) {
            TextView name = Ui.label(a, from.getName(), 15, Ui.text(a), true);
            box.addView(name);
            box.addView(Ui.oneLine(Ui.small(a, from.getUrl())));
            box.addView(Ui.space(a, 8));
        }
        box.addView(Ui.small(a, a.getString(R.string.relink_body)));
        EditText input = Ui.input(a, a.getString(R.string.relink_hint), InputType.TYPE_CLASS_TEXT
                | InputType.TYPE_TEXT_VARIATION_URI | InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS);
        if (prefill != null) input.setText(prefill);
        box.addView(input, Ui.margins(a, Ui.MATCH, Ui.WRAP, 0, 10, 0, 4));
        if (error != null) {
            TextView problem = Ui.small(a, error);
            problem.setTextColor(Ui.danger(a));
            box.addView(problem);
        }
        TextView note = Ui.small(a, a.getString(R.string.relink_network_note));
        note.setTextColor(Ui.faint(a));
        note.setPadding(0, Ui.dp(a, 8), 0, Ui.dp(a, 4));
        box.addView(note);

        AlertDialog dialog = new AlertDialog.Builder(a)
                .setTitle(R.string.relink_title)
                .setView(box)
                .setNegativeButton(R.string.cancel, null)
                .setNeutralButton(R.string.relink_scan, (d, w) -> host.scanForRelink(fromUrl))
                .setPositiveButton(R.string.relink, null)
                .create();
        dialog.setOnShowListener(d -> dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener(v -> {
            String text = input.getText().toString();
            PairingLink link = PairingLink.parseRelinkInput(text);
            if (link == null) {
                input.setError(a.getString(R.string.relink_invalid));
                return;
            }
            dialog.dismiss();
            relink(link, fromUrl, text.trim());
        }));
        dialog.show();
    }

    /**
     * Checks {@code link}'s address and moves the saved server there. The
     * server id comes from the re-link QR, else from the server the dialog was
     * opened for, else from the answer; the signature must verify with the key
     * stored for that id, and the id and host must match.
     */
    void relink(PairingLink link, String fromUrl, String typed) {
        String base = link.getBaseUrl();
        ServerStore store = Accounts.servers(a);
        ServerEntry from = fromUrl == null ? null : store.get(fromUrl);
        String wanted = link.getRelinkId() != null ? link.getRelinkId() : from != null ? from.getServerId() : null;
        if (link.getRelinkId() == null && from != null && !from.hasIdentity()) {
            refuse(a.getString(R.string.relink_no_key, from.getName()), fromUrl, typed);
            return;
        }
        if (wanted != null && store.findByServerId(wanted) == null) {
            refuse(a.getString(R.string.relink_unknown), null, null);
            return;
        }
        if (PairingLink.isInsecurePublic(base)) {
            refuse(a.getString(R.string.relink_https_only), fromUrl, typed);
            return;
        }
        String where = ServerUrl.displayHost(base);
        Toast.makeText(a, a.getString(R.string.relink_checking, where), Toast.LENGTH_SHORT).show();
        String nonce = ServerIdentity.newNonce();
        Async.run(a, () -> ArkClient.identity(base, nonce), answer -> {
            ServerStore now = Accounts.servers(a);
            String id = wanted != null ? wanted : answer.optString("server_id", "");
            ServerEntry target = from != null && id.equals(from.getServerId()) ? from : now.findByServerId(id);
            if (target == null) {
                refuse(a.getString(R.string.relink_unknown), null, null);
                return;
            }
            if (ServerUrl.sameServer(target.getUrl(), base)) {
                Toast.makeText(a, a.getString(R.string.relink_same, target.getName()), Toast.LENGTH_LONG).show();
                host.changed(target.getUrl());
                return;
            }
            ServerIdentity.Check check = ServerIdentity.check(base, nonce, answer, target.getServerId(),
                    target.getPublicKey());
            if (!check.ok()) {
                boolean other = check.problem == ServerIdentity.Problem.WRONG_SERVER
                        || check.problem == ServerIdentity.Problem.WRONG_KEY;
                refuse(a.getString(other ? R.string.relink_other_server : R.string.relink_bad_signature,
                        where, target.getName()), fromUrl, typed);
                return;
            }
            ServerEntry moved;
            try {
                moved = Accounts.relink(a, target.getUrl(), base);
            } catch (Exception failed) {
                refuse(a.getString(R.string.secure_store_failed, String.valueOf(failed.getMessage())), null, null);
                return;
            }
            if (moved == null) return;
            Toast.makeText(a, a.getString(R.string.relink_done, ServerUrl.displayHost(moved.getUrl())),
                    Toast.LENGTH_LONG).show();
            host.changed(moved.getUrl());
        }, error -> {
            if (error.isUnreachable()) {
                refuse(a.getString(R.string.relink_unreachable, where), fromUrl, typed);
            } else {
                refuse(a.getString(R.string.relink_no_identity, where), fromUrl, typed);
            }
        });
    }

    /** Explains why nothing moved; offers the dialog again when it was opened from one. */
    private void refuse(String message, String fromUrl, String typed) {
        AlertDialog.Builder b = new AlertDialog.Builder(a)
                .setTitle(R.string.relink_failed)
                .setMessage(message)
                .setPositiveButton(android.R.string.ok, null);
        if (typed != null) b.setNeutralButton(R.string.relink, (d, w) -> showRelink(fromUrl, typed, null));
        b.show();
    }
}

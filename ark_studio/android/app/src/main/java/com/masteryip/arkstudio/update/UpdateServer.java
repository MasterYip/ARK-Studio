package com.masteryip.arkstudio.update;

import android.content.Context;

import com.masteryip.arkstudio.data.AccountSession;
import com.masteryip.arkstudio.platform.Accounts;

/** Where the shared updater asks first: the signed-in server of the native app. */
final class UpdateServer {
    private UpdateServer() {
    }

    static String base(Context c) {
        AccountSession session = Accounts.active(c);
        return session == null ? "" : session.serverUrl;
    }
}

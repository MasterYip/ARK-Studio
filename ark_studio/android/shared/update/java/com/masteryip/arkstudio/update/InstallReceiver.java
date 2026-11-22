package com.masteryip.arkstudio.update;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageInstaller;
import android.widget.Toast;

import com.masteryip.arkstudio.R;

/**
 * Status of a {@link PackageInstaller} session: asks the person to confirm
 * (Android always does for an app installing its own update) and reports a
 * failure. On success Android restarts nothing; the new version opens next time.
 */
public final class InstallReceiver extends BroadcastReceiver {
    @Override
    public void onReceive(Context context, Intent intent) {
        if (!Updater.ACTION_INSTALLED.equals(intent.getAction())) return;
        int status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE);
        if (status == PackageInstaller.STATUS_PENDING_USER_ACTION) {
            Intent confirm = intent.getParcelableExtra(Intent.EXTRA_INTENT);
            if (confirm != null) {
                confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
                context.startActivity(confirm);
            }
            return;
        }
        if (status == PackageInstaller.STATUS_SUCCESS) return;
        String message = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE);
        int text = status == PackageInstaller.STATUS_FAILURE_CONFLICT || status == PackageInstaller.STATUS_FAILURE_INCOMPATIBLE
                ? R.string.update_mismatch_title : R.string.update_failed;
        if (status == PackageInstaller.STATUS_FAILURE_ABORTED) return; // the person cancelled
        Toast.makeText(context, context.getString(text, message == null ? "" : message), Toast.LENGTH_LONG).show();
    }
}

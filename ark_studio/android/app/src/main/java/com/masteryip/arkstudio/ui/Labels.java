package com.masteryip.arkstudio.ui;

import android.content.Context;

import com.masteryip.arkstudio.R;

import java.util.HashMap;
import java.util.Map;

/**
 * Display names for the codes the server sends (unit types, statuses, link relations,
 * setup states, permissions, activity actions). Unknown codes fall back to
 * {@link Fmt#human(String)}, so a newer server still shows something readable.
 */
public final class Labels {
    private Labels() {
    }

    private static final Map<String, Integer> CODES = new HashMap<>();

    static {
        // Unit types
        put("idea", R.string.label_idea);
        put("question", R.string.label_question);
        put("claim", R.string.label_claim);
        put("evidence", R.string.label_evidence);
        put("story", R.string.label_story);
        put("related_work", R.string.label_related_work);
        put("method", R.string.label_method);
        put("experiment", R.string.label_experiment);
        put("topic", R.string.label_topic);
        put("run", R.string.label_run);
        put("task", R.string.label_task);
        put("milestone", R.string.label_milestone);
        put("artifact", R.string.label_artifact);
        put("resource", R.string.label_resource);
        put("agent", R.string.label_agent);
        put("host", R.string.label_host);
        put("production", R.string.label_production);
        put("outreach", R.string.label_outreach);
        put("decision", R.string.label_decision);
        put("source", R.string.label_source);
        put("note", R.string.label_note);
        // Unit statuses
        put("inbox", R.string.status_inbox);
        put("open", R.string.status_open);
        put("active", R.string.status_active);
        put("review", R.string.status_review);
        put("blocked", R.string.status_blocked);
        put("done", R.string.status_done);
        put("abandoned", R.string.label_abandoned);
        put("archived", R.string.status_archived);
        // Run, GPU job, steer and approval states
        put("running", R.string.run_running);
        put("idle", R.string.run_idle);
        put("starting", R.string.run_starting);
        put("stopping", R.string.run_stopping);
        put("errored", R.string.run_errored);
        put("stopped", R.string.run_stopped);
        put("queued", R.string.label_queued);
        put("completed", R.string.label_completed);
        put("canceled", R.string.label_canceled);
        put("cancelled", R.string.label_canceled);
        put("delivered", R.string.label_delivered);
        put("failed", R.string.label_failed);
        put("pending", R.string.label_pending);
        put("deciding", R.string.label_deciding);
        put("allowed", R.string.label_allowed);
        put("allow", R.string.label_allowed);
        put("denied", R.string.label_denied);
        put("deny", R.string.label_denied);
        put("expired", R.string.label_expired);
        put("resolved", R.string.label_resolved);
        // Link relations
        put("part_of", R.string.label_part_of);
        put("derived_from", R.string.label_derived_from);
        put("supports", R.string.label_supports);
        put("contradicts", R.string.label_contradicts);
        put("depends_on", R.string.label_depends_on);
        put("blocks", R.string.label_blocks);
        put("cites", R.string.label_cites);
        put("inspires", R.string.label_inspires);
        put("targets", R.string.label_targets);
        put("produces", R.string.label_produces);
        put("validates", R.string.label_validates);
        put("assigned_to", R.string.label_assigned_to);
        put("runs_on", R.string.label_runs_on);
        put("supersedes", R.string.label_supersedes);
        put("uses", R.string.label_uses);
        put("relates_to", R.string.label_relates_to);
        // Setup states
        put("unset", R.string.label_unset);
        put("draft", R.string.label_draft);
        put("proposed", R.string.label_proposed);
        put("stale", R.string.label_stale);
        put("confirmed", R.string.label_confirmed);
        put("verified", R.string.label_verified);
        // Permissions
        put("read", R.string.perm_read_only);
        put("read_only", R.string.perm_read_only);
        put("write", R.string.label_write);
        put("full_access", R.string.perm_full);
        put("admin", R.string.label_admin);
        // Home "needs attention" kinds
        put("overdue", R.string.count_overdue);
        put("assign", R.string.label_assign);
        // Media kinds
        put("image", R.string.image);
        put("video", R.string.video);
        put("audio", R.string.label_audio);
        put("file", R.string.label_file);
        // Activity log actions
        put("unit.create", R.string.log_unit_create);
        put("unit.update", R.string.log_unit_update);
        put("unit.archive", R.string.log_unit_archive);
        put("unit.restore", R.string.log_unit_restore);
        put("unit.purge", R.string.log_unit_purge);
        put("unit.history", R.string.log_unit_history);
        put("unit.backfill", R.string.log_unit_backfill);
        put("unit.needs", R.string.log_unit_needs);
        put("topic.update", R.string.log_topic_update);
        put("link.create", R.string.log_link_create);
        put("link.delete", R.string.log_link_delete);
        put("media.create", R.string.log_media_create);
        put("media.update", R.string.log_media_update);
        put("media.delete", R.string.log_media_delete);
        put("comment.create", R.string.log_comment_create);
        put("comment.resolve", R.string.log_comment_resolve);
        put("comment.reopen", R.string.log_comment_reopen);
        put("comment.migrate", R.string.log_comment_migrate);
        put("import.infohub", R.string.log_import_infohub);
        put("project.create", R.string.log_project_create);
        put("project.update", R.string.log_project_update);
        put("project.settings", R.string.log_project_settings);
        put("assistant.apply", R.string.log_assistant_apply);
    }

    private static void put(String code, int resId) {
        CODES.put(code, resId);
    }

    /** The localized label for a server code, or a humanized fallback. */
    public static String of(Context c, String code) {
        Integer id = code == null ? null : CODES.get(code);
        return id != null ? c.getString(id) : Fmt.human(code);
    }
}

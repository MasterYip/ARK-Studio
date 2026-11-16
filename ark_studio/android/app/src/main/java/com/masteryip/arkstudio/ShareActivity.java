package com.masteryip.arkstudio;

import android.content.Intent;
import android.database.Cursor;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.net.Uri;
import android.os.Bundle;
import android.provider.OpenableColumns;
import android.util.Base64;
import android.widget.Button;
import android.widget.EditText;
import android.widget.ImageView;
import android.widget.RadioButton;
import android.widget.RadioGroup;
import android.widget.TextView;

import com.masteryip.arkstudio.api.ApiException;
import com.masteryip.arkstudio.model.WebRoutes;
import com.masteryip.arkstudio.ui.Ui;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.util.Locale;

/**
 * Share-to-ARK: text, a link or an image from another app becomes an idea or
 * note unit ({@code POST /api/units}); an image is attached with
 * {@code POST /api/media/upload} (base64 JSON, 12 MB, jpg/png/gif/webp/svg).
 */
public class ShareActivity extends BaseActivity {
    private static final long MAX_IMAGE = 12L * 1024 * 1024;

    private Uri image;
    private String imageMime;
    private EditText title;
    private EditText body;
    private RadioGroup kind;
    private Button save;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        if (!ready()) return;
        buildFrame(getString(R.string.share_title), true);
        setSubtitle(session.projectName + " · " + MainActivity.displayUser(session));
        Intent intent = getIntent();
        String text = intent.getStringExtra(Intent.EXTRA_TEXT);
        String subject = intent.getStringExtra(Intent.EXTRA_SUBJECT);
        if (Intent.ACTION_SEND.equals(intent.getAction()) && intent.getType() != null
                && intent.getType().startsWith("image/")) {
            image = intent.getParcelableExtra(Intent.EXTRA_STREAM);
            imageMime = intent.getType();
        }
        render(subject, text);
    }

    private void render(String subject, String text) {
        content.removeAllViews();
        if (session.isReadOnly()) {
            content.addView(Ui.body(this, getString(R.string.share_read_only)));
            return;
        }
        kind = new RadioGroup(this);
        kind.setOrientation(RadioGroup.HORIZONTAL);
        RadioButton idea = new RadioButton(this);
        idea.setText(R.string.type_idea);
        idea.setId(android.view.View.generateViewId());
        idea.setTextColor(Ui.text(this));
        RadioButton note = new RadioButton(this);
        note.setText(R.string.type_note);
        note.setId(android.view.View.generateViewId());
        note.setTextColor(Ui.text(this));
        kind.addView(idea);
        kind.addView(note);
        kind.check(idea.getId());
        content.addView(kind);

        content.addView(Ui.small(this, getString(R.string.share_title_label)));
        title = Ui.input(this, getString(R.string.share_title_label),
                android.text.InputType.TYPE_CLASS_TEXT | android.text.InputType.TYPE_TEXT_FLAG_CAP_SENTENCES);
        title.setText(defaultTitle(subject, text));
        content.addView(title);
        content.addView(Ui.small(this, getString(R.string.share_body_label)));
        body = Ui.multiline(this, getString(R.string.share_body_label));
        body.setMaxLines(10);
        body.setText(text == null ? "" : text);
        content.addView(body);
        if (image != null) {
            ImageView preview = new ImageView(this);
            preview.setAdjustViewBounds(true);
            preview.setMaxHeight(Ui.dp(this, 260));
            preview.setContentDescription(getString(R.string.image));
            try (InputStream in = getContentResolver().openInputStream(image)) {
                BitmapFactory.Options options = new BitmapFactory.Options();
                options.inSampleSize = 4;
                Bitmap bitmap = BitmapFactory.decodeStream(in, null, options);
                preview.setImageBitmap(bitmap);
            } catch (Exception unreadable) {
                preview.setImageResource(R.drawable.ic_ark_prism);
            }
            content.addView(preview, Ui.margins(this, Ui.MATCH, Ui.WRAP, 0, 4, 0, 8));
        }
        save = Ui.button(this, getString(R.string.share_save), true, false);
        save.setOnClickListener(v -> submit(kind.getCheckedRadioButtonId() == idea.getId() ? "idea" : "note"));
        content.addView(save, Ui.margins(this, Ui.MATCH, Ui.WRAP, 0, 8, 0, 4));
        TextView where = Ui.small(this, getString(R.string.share_where, session.projectName, session.serverUrl));
        content.addView(where);
    }

    static String defaultTitle(String subject, String text) {
        if (subject != null && !subject.trim().isEmpty()) return subject.trim();
        if (text == null || text.trim().isEmpty()) return "";
        String first = text.trim().split("\n", 2)[0];
        return first.length() > 80 ? first.substring(0, 77) + "…" : first;
    }

    private void submit(String type) {
        String name = title.getText().toString().trim();
        String text = body.getText().toString().trim();
        if (name.isEmpty()) name = image != null ? getString(R.string.shared_image) : defaultTitle(null, text);
        if (name.isEmpty()) {
            title.setError(getString(R.string.required));
            return;
        }
        save.setEnabled(false);
        final String finalName = name;
        call(() -> {
            JSONObject unitBody = new JSONObject()
                    .put("type", type)
                    .put("title", finalName)
                    .put("body", text)
                    .put("tags", new JSONArray().put("mobile").put("shared"));
            JSONObject unit = api.post("/api/units", unitBody).getJSONObject("unit");
            if (image != null) upload(unit.optString("uid", String.valueOf(unit.opt("id"))));
            return unit;
        }, unit -> {
            toast(getString(R.string.share_saved, unit.optString("uid")));
            startActivity(NodeActivity.intent(this, unit.optString("uid")));
            finish();
        });
        save.postDelayed(() -> {
            if (!isFinishing()) save.setEnabled(true);
        }, 5000);
    }

    private void upload(String unit) throws Exception {
        byte[] bytes;
        try (InputStream in = getContentResolver().openInputStream(image)) {
            if (in == null) throw new ApiException(-1, "CLIENT", getString(R.string.share_image_unreadable));
            ByteArrayOutputStream out = new ByteArrayOutputStream();
            byte[] buffer = new byte[16384];
            int read;
            while ((read = in.read(buffer)) != -1) {
                out.write(buffer, 0, read);
                if (out.size() > MAX_IMAGE) {
                    throw new ApiException(413, "TOO_LARGE", getString(R.string.share_image_too_large));
                }
            }
            bytes = out.toByteArray();
        }
        JSONObject upload = new JSONObject()
                .put("unit", unit)
                .put("filename", fileName())
                .put("mime", imageMime)
                .put("data", Base64.encodeToString(bytes, Base64.NO_WRAP))
                .put("caption", "");
        api.post("/api/media/upload", upload);
    }

    /** Display name with an extension the server accepts. */
    private String fileName() {
        String name = null;
        try (Cursor cursor = getContentResolver().query(image, new String[]{OpenableColumns.DISPLAY_NAME},
                null, null, null)) {
            if (cursor != null && cursor.moveToFirst()) name = cursor.getString(0);
        } catch (Exception ignored) {
            name = null;
        }
        String ext = extension(imageMime);
        if (name == null || name.trim().isEmpty()) name = "shared-" + System.currentTimeMillis();
        String lower = name.toLowerCase(Locale.ROOT);
        if (!(lower.endsWith(".jpg") || lower.endsWith(".jpeg") || lower.endsWith(".png") || lower.endsWith(".gif")
                || lower.endsWith(".webp") || lower.endsWith(".svg"))) {
            name = name + "." + ext;
        }
        return name;
    }

    static String extension(String mime) {
        String m = mime == null ? "" : mime.toLowerCase(Locale.ROOT);
        if (m.contains("png")) return "png";
        if (m.contains("gif")) return "gif";
        if (m.contains("webp")) return "webp";
        if (m.contains("svg")) return "svg";
        return "jpg";
    }

    @Override
    protected void reload() {
        doneRefreshing();
    }

    @Override
    public void openWeb(String route) {
        super.openWeb(route == null ? WebRoutes.IDEAS : route);
    }
}

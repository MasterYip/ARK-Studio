package com.masteryip.arkstudio;

import android.content.Context;
import android.content.Intent;
import android.graphics.Color;
import android.graphics.Matrix;
import android.graphics.RectF;
import android.graphics.drawable.Drawable;
import android.net.Uri;
import android.os.Bundle;
import android.view.Gravity;
import android.view.MotionEvent;
import android.view.ScaleGestureDetector;
import android.widget.FrameLayout;
import android.widget.ImageView;
import android.widget.MediaController;
import android.widget.TextView;
import android.widget.VideoView;

import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.data.PairingLink;
import com.masteryip.arkstudio.ui.Images;
import com.masteryip.arkstudio.ui.Ui;

import java.util.HashMap;
import java.util.Locale;
import java.util.Map;

/**
 * Full-screen media: pinch-zoom image viewer, or a video player streaming
 * {@code /api/media/{id}/raw} (the server honours Range, so seeking works).
 * The device token goes in a request header, and only to the server's origin.
 */
public class MediaActivity extends BaseActivity {
    private static final String EXTRA_URL = "url";
    private static final String EXTRA_CAPTION = "caption";
    private static final String EXTRA_KIND = "kind";

    private VideoView video;
    private int position;

    public static Intent intent(Context c, String url, String caption, String kind) {
        return new Intent(c, MediaActivity.class).putExtra(EXTRA_URL, url).putExtra(EXTRA_CAPTION, caption)
                .putExtra(EXTRA_KIND, kind);
    }

    static boolean isVideo(String kind, String url) {
        String k = kind == null ? "" : kind.toLowerCase(Locale.ROOT);
        if (k.equals("video")) return true;
        String u = url == null ? "" : url.toLowerCase(Locale.ROOT);
        int q = u.indexOf('?');
        if (q >= 0) u = u.substring(0, q);
        return u.endsWith(".mp4") || u.endsWith(".webm") || u.endsWith(".mov") || u.endsWith(".m4v")
                || u.endsWith(".mkv");
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        if (!ready()) return;
        String url = getIntent().getStringExtra(EXTRA_URL);
        String caption = getIntent().getStringExtra(EXTRA_CAPTION);
        String kind = getIntent().getStringExtra(EXTRA_KIND);
        buildFrame(caption == null || caption.isEmpty() ? getString(R.string.media) : caption, true);
        addAction(getString(R.string.open_externally), v -> Links.external(this, url));
        stage.removeAllViews();
        stage.setBackgroundColor(Color.BLACK);
        if (savedInstanceState != null) position = savedInstanceState.getInt("position", 0);
        if ("image".equals(kind) || (kind == null && !isVideo(null, url))) showImage(url);
        else showVideo(url);
    }

    private void showImage(String url) {
        ImageView image = new ImageView(this);
        image.setScaleType(ImageView.ScaleType.MATRIX);
        image.setContentDescription(getString(R.string.image));
        stage.addView(image, new FrameLayout.LayoutParams(Ui.MATCH, Ui.MATCH));
        Images.load(this, api, url, 2560, image);
        new Zoom(image);
    }

    private void showVideo(String url) {
        video = new VideoView(this);
        FrameLayout.LayoutParams params = new FrameLayout.LayoutParams(Ui.MATCH, Ui.WRAP, Gravity.CENTER);
        stage.addView(video, params);
        MediaController controller = new MediaController(this);
        controller.setAnchorView(video);
        video.setMediaController(controller);
        Map<String, String> headers = new HashMap<>();
        if (PairingLink.sameOrigin(session.serverUrl, url)) headers.put(ArkClient.TOKEN_HEADER, session.token);
        video.setVideoURI(Uri.parse(url), headers);
        video.setOnPreparedListener(player -> {
            if (position > 0) video.seekTo(position);
            video.start();
            controller.show(3000);
        });
        video.setOnErrorListener((player, what, extra) -> {
            TextView error = Ui.label(this, getString(R.string.video_failed), 15, Color.WHITE, false);
            error.setGravity(Gravity.CENTER);
            stage.addView(error, new FrameLayout.LayoutParams(Ui.MATCH, Ui.WRAP, Gravity.BOTTOM));
            return true;
        });
    }

    @Override
    protected void onPause() {
        super.onPause();
        if (video != null) {
            position = video.getCurrentPosition();
            video.pause();
        }
    }

    @Override
    protected void onSaveInstanceState(Bundle outState) {
        super.onSaveInstanceState(outState);
        outState.putInt("position", video == null ? 0 : video.getCurrentPosition());
    }

    @Override
    protected void reload() {
        doneRefreshing();
    }

    /** Fit-to-screen, pinch zoom, drag and double-tap reset. */
    private static final class Zoom {
        private final ImageView view;
        private final Matrix matrix = new Matrix();
        private final ScaleGestureDetector scaler;
        private float lastX;
        private float lastY;
        private long lastTap;

        Zoom(ImageView view) {
            this.view = view;
            scaler = new ScaleGestureDetector(view.getContext(), new ScaleGestureDetector.SimpleOnScaleGestureListener() {
                @Override
                public boolean onScale(ScaleGestureDetector detector) {
                    matrix.postScale(detector.getScaleFactor(), detector.getScaleFactor(),
                            detector.getFocusX(), detector.getFocusY());
                    view.setImageMatrix(matrix);
                    return true;
                }
            });
            view.addOnLayoutChangeListener((v, l, t, r, b, ol, ot, or, ob) -> fit());
            view.setOnTouchListener((v, event) -> {
                scaler.onTouchEvent(event);
                switch (event.getActionMasked()) {
                    case MotionEvent.ACTION_DOWN:
                        lastX = event.getX();
                        lastY = event.getY();
                        long now = System.currentTimeMillis();
                        if (now - lastTap < 300) fit();
                        lastTap = now;
                        break;
                    case MotionEvent.ACTION_MOVE:
                        if (!scaler.isInProgress() && event.getPointerCount() == 1) {
                            matrix.postTranslate(event.getX() - lastX, event.getY() - lastY);
                            view.setImageMatrix(matrix);
                        }
                        lastX = event.getX();
                        lastY = event.getY();
                        break;
                    default:
                        break;
                }
                return true;
            });
        }

        void fit() {
            Drawable d = view.getDrawable();
            if (d == null || view.getWidth() == 0) return;
            matrix.setRectToRect(new RectF(0, 0, d.getIntrinsicWidth(), d.getIntrinsicHeight()),
                    new RectF(0, 0, view.getWidth(), view.getHeight()), Matrix.ScaleToFit.CENTER);
            view.setImageMatrix(matrix);
        }
    }
}

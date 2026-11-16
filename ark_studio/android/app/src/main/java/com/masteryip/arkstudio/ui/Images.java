package com.masteryip.arkstudio.ui;

import android.app.Activity;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.util.LruCache;
import android.widget.ImageView;

import com.masteryip.arkstudio.api.ApiException;
import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.data.PairingLink;

import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;

/** Authenticated image loading (token in header, same origin only) with a small memory cache. */
public final class Images {
    private static final LruCache<String, Bitmap> CACHE = new LruCache<String, Bitmap>(24 * 1024 * 1024) {
        @Override
        protected int sizeOf(String key, Bitmap value) {
            return value.getByteCount();
        }
    };

    private Images() {
    }

    public static void load(Activity owner, ArkClient api, String url, int maxPx, ImageView target) {
        String key = url + "@" + maxPx;
        Bitmap cached = CACHE.get(key);
        if (cached != null) {
            target.setImageBitmap(cached);
            return;
        }
        target.setTag(key);
        Async.run(owner, () -> fetch(api, url, maxPx), bitmap -> {
            if (bitmap == null) return;
            CACHE.put(key, bitmap);
            if (key.equals(target.getTag())) target.setImageBitmap(bitmap);
        }, error -> {
            // leave the placeholder
        });
    }

    static Bitmap fetch(ArkClient api, String url, int maxPx) throws Exception {
        HttpURLConnection connection = (HttpURLConnection) new URL(url).openConnection();
        try {
            connection.setConnectTimeout(8000);
            connection.setReadTimeout(30000);
            connection.setRequestProperty("User-Agent", ArkClient.USER_AGENT);
            // Never send the device token to another origin.
            if (PairingLink.sameOrigin(api.getBase(), url) && !api.getToken().isEmpty()) {
                connection.setRequestProperty(ArkClient.TOKEN_HEADER, api.getToken());
            }
            int status = connection.getResponseCode();
            if (status >= 400) throw new ApiException(status, "HTTP_" + status, "HTTP " + status);
            byte[] data;
            try (InputStream in = connection.getInputStream()) {
                ByteArrayOutputStream out = new ByteArrayOutputStream();
                byte[] buffer = new byte[16384];
                int read;
                long total = 0;
                while ((read = in.read(buffer)) != -1) {
                    total += read;
                    if (total > 40L * 1024 * 1024) throw new ApiException(413, "TOO_LARGE", "Image too large");
                    out.write(buffer, 0, read);
                }
                data = out.toByteArray();
            }
            BitmapFactory.Options bounds = new BitmapFactory.Options();
            bounds.inJustDecodeBounds = true;
            BitmapFactory.decodeByteArray(data, 0, data.length, bounds);
            int sample = 1;
            while (bounds.outWidth / (sample * 2) >= maxPx || bounds.outHeight / (sample * 2) >= maxPx) sample *= 2;
            BitmapFactory.Options options = new BitmapFactory.Options();
            options.inSampleSize = sample;
            return BitmapFactory.decodeByteArray(data, 0, data.length, options);
        } finally {
            connection.disconnect();
        }
    }
}

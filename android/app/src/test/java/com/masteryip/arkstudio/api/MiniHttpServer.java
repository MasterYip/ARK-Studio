package com.masteryip.arkstudio.api;

import java.io.BufferedInputStream;
import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.util.LinkedHashMap;
import java.util.Locale;
import java.util.Map;

/** One-connection-at-a-time HTTP/1.1 server for client tests (no JDK internals). */
final class MiniHttpServer {
    static final class Request {
        String method;
        String target;
        final Map<String, String> headers = new LinkedHashMap<>();
        String body = "";

        String path() {
            int q = target.indexOf('?');
            return q < 0 ? target : target.substring(0, q);
        }

        String header(String name) {
            return headers.get(name.toLowerCase(Locale.ROOT));
        }
    }

    static final class Response {
        int status = 200;
        String body = "";
        final Map<String, String> headers = new LinkedHashMap<>();

        Response(int status, String body) {
            this.status = status;
            this.body = body;
        }

        Response header(String name, String value) {
            headers.put(name, value);
            return this;
        }
    }

    interface Handler {
        Response handle(Request request) throws Exception;
    }

    private final ServerSocket socket;
    private final Thread thread;
    private volatile boolean running = true;

    MiniHttpServer(Handler handler) throws IOException {
        socket = new ServerSocket(0, 50, InetAddress.getByName("127.0.0.1"));
        thread = new Thread(() -> {
            while (running) {
                try (Socket client = socket.accept()) {
                    serve(client, handler);
                } catch (Exception ignored) {
                    // closed or a broken client; keep serving until stop()
                }
            }
        });
        thread.setDaemon(true);
        thread.start();
    }

    int port() {
        return socket.getLocalPort();
    }

    void stop() {
        running = false;
        try {
            socket.close();
        } catch (IOException ignored) {
            // already closed
        }
    }

    private static void serve(Socket client, Handler handler) throws Exception {
        InputStream in = new BufferedInputStream(client.getInputStream());
        Request request = new Request();
        String line = readLine(in);
        if (line == null || line.isEmpty()) return;
        String[] parts = line.split(" ");
        request.method = parts[0];
        request.target = parts.length > 1 ? parts[1] : "/";
        while ((line = readLine(in)) != null && !line.isEmpty()) {
            int colon = line.indexOf(':');
            if (colon > 0) {
                request.headers.put(line.substring(0, colon).trim().toLowerCase(Locale.ROOT),
                        line.substring(colon + 1).trim());
            }
        }
        String length = request.header("content-length");
        if (length != null) {
            int n = Integer.parseInt(length);
            byte[] body = new byte[n];
            int read = 0;
            while (read < n) {
                int r = in.read(body, read, n - read);
                if (r < 0) break;
                read += r;
            }
            request.body = new String(body, 0, read, StandardCharsets.UTF_8);
        }
        Response response = handler.handle(request);
        byte[] bytes = response.body.getBytes(StandardCharsets.UTF_8);
        StringBuilder head = new StringBuilder();
        head.append("HTTP/1.1 ").append(response.status).append(" X\r\n");
        head.append("Content-Type: application/json\r\n");
        head.append("Content-Length: ").append(bytes.length).append("\r\n");
        head.append("Connection: close\r\n");
        for (Map.Entry<String, String> entry : response.headers.entrySet()) {
            head.append(entry.getKey()).append(": ").append(entry.getValue()).append("\r\n");
        }
        head.append("\r\n");
        OutputStream out = client.getOutputStream();
        out.write(head.toString().getBytes(StandardCharsets.UTF_8));
        out.write(bytes);
        out.flush();
    }

    private static String readLine(InputStream in) throws IOException {
        ByteArrayOutputStream buffer = new ByteArrayOutputStream();
        int c;
        while ((c = in.read()) != -1) {
            if (c == '\n') break;
            if (c != '\r') buffer.write(c);
        }
        if (c == -1 && buffer.size() == 0) return null;
        return new String(buffer.toByteArray(), StandardCharsets.UTF_8);
    }
}

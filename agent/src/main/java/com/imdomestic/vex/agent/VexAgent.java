package com.imdomestic.vex.agent;

import com.google.gson.JsonArray;
import com.google.gson.JsonElement;
import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import com.mojang.brigadier.arguments.StringArgumentType;
import java.io.IOException;
import java.net.URI;
import java.net.URLEncoder;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.FutureTask;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicBoolean;
import net.fabricmc.api.DedicatedServerModInitializer;
import net.fabricmc.fabric.api.command.v2.CommandRegistrationCallback;
import net.fabricmc.fabric.api.event.lifecycle.v1.ServerLifecycleEvents;
import net.fabricmc.fabric.api.networking.v1.ServerPlayConnectionEvents;
import net.minecraft.commands.Commands;
import net.minecraft.network.chat.ClickEvent;
import net.minecraft.network.chat.Component;
import net.minecraft.network.protocol.common.ClientboundTransferPacket;
import net.minecraft.server.MinecraftServer;
import net.minecraft.server.level.ServerPlayer;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

public final class VexAgent implements DedicatedServerModInitializer {
    private static final Logger LOG = LoggerFactory.getLogger("vex-agent");
    private final ExecutorService network = Executors.newVirtualThreadPerTaskExecutor();
    private final HttpClient http = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(5)).followRedirects(HttpClient.Redirect.NEVER).build();
    private final ConcurrentHashMap<UUID, FutureTask<Void>> pending = new ConcurrentHashMap<>();
    private String roomID;
    private String project;
    private String buildID;
    private long generation;
    private String token;
    private URI base;

    private static String required(String name) {
        String value = System.getenv(name);
        if (value == null || value.isBlank()) throw new IllegalStateException("缺少环境变量 " + name);
        return value;
    }

    @Override public void onInitializeServer() {
        project = required("VEX_PROJECT");
        if (!project.matches("[a-z][a-z0-9-]{0,22}") || project.endsWith("-") || project.contains("--")) throw new IllegalStateException("VEX_PROJECT 无效");
        roomID = required("VEX_ROOM_ID");
        buildID = required("VEX_BUILD_ID");
        generation = Long.parseLong(required("VEX_GENERATION"));
        if (generation < 1) throw new IllegalStateException("VEX_GENERATION 无效");
        token = required("VEX_ROOM_TOKEN");
        base = URI.create(required("VEX_CONTROLLER_URL"));
        if (!("http".equals(base.getScheme()) || "https".equals(base.getScheme())) || base.getUserInfo() != null || base.getQuery() != null || base.getFragment() != null || base.getHost() == null || !(base.getPath().isEmpty() || base.getPath().equals("/"))) {
            throw new IllegalStateException("VEX_CONTROLLER_URL 无效");
        }
        CommandRegistrationCallback.EVENT.register((dispatcher, registry, environment) -> dispatcher.register(
            Commands.literal("server")
                .executes(context -> {
                    ServerPlayer player = context.getSource().getPlayerOrException();
                    list(context.getSource().getServer(), player);
                    return 1;
                })
                .then(Commands.argument("destination", Protocol.destinationArgument()).executes(context -> {
                    ServerPlayer player = context.getSource().getPlayerOrException();
                    begin(context.getSource().getServer(), player, Protocol.destination(StringArgumentType.getString(context, "destination")));
                    return 1;
                }))));
        ServerPlayConnectionEvents.DISCONNECT.register((handler, server) -> {
            FutureTask<Void> task = pending.remove(handler.getPlayer().getUUID());
            if (task != null) task.cancel(true);
        });
        ServerLifecycleEvents.SERVER_STOPPING.register(server -> {
            pending.values().forEach(task -> task.cancel(true));
            network.shutdownNow();
        });
        LOG.info("Vex Agent 已注册项目 {} 房间 {} 第 {} 轮的 /server 命令", project, roomID, generation);
    }

    private static String segment(String value) { return URLEncoder.encode(value, StandardCharsets.UTF_8).replace("+", "%20"); }
    private String projectPath(String suffix) { return "/v2/projects/" + project + suffix; }

    private HttpRequest.Builder request(String path) {
        return HttpRequest.newBuilder(base.resolve(path)).timeout(Duration.ofSeconds(10))
            .header("Authorization", "Bearer " + token).header("Accept", "application/json");
    }

    private JsonElement call(String method, String path, JsonObject body) throws IOException, InterruptedException {
        return call(method, path, body, Duration.ofSeconds(10));
    }

    private JsonElement call(String method, String path, JsonObject body, Duration timeout) throws IOException, InterruptedException {
        HttpRequest.Builder builder = request(path).timeout(timeout);
        builder.method(method, body == null ? HttpRequest.BodyPublishers.noBody() : HttpRequest.BodyPublishers.ofString(body.toString()));
        if (body != null) builder.header("Content-Type", "application/json");
        var responseFuture = http.sendAsync(builder.build(), HttpResponse.BodyHandlers.limiting(HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8), 1024 * 1024));
        HttpResponse<String> response;
        try {
            response = responseFuture.get(timeout.toNanos(), TimeUnit.NANOSECONDS);
        } catch (TimeoutException error) {
            throw new IOException("控制器响应超时", error);
        } catch (ExecutionException error) {
            throw new IOException("控制器请求失败", error.getCause());
        } finally {
            responseFuture.cancel(true);
        }
        JsonElement json = JsonParser.parseString(response.body());
        if (response.statusCode() < 200 || response.statusCode() >= 300) {
            String message = "控制器请求失败：HTTP " + response.statusCode();
            if (json.isJsonObject() && json.getAsJsonObject().has("error")) {
                message = Protocol.text(json.getAsJsonObject().getAsJsonObject("error"), "message");
            }
            throw new IOException(message);
        }
        return json;
    }

    private static void message(MinecraftServer server, ServerPlayer player, String message, boolean overlay) {
        server.execute(() -> { if (!player.hasDisconnected()) player.sendSystemMessage(Component.literal(message), overlay); });
    }

    private void list(MinecraftServer server, ServerPlayer player) {
        network.execute(() -> {
            try {
                JsonArray destinations = call("GET", projectPath("/hop/destinations?fromRoom=" + segment(roomID)), null).getAsJsonArray();
                server.execute(() -> {
                    if (player.hasDisconnected()) return;
                    player.sendSystemMessage(Component.literal(destinations.isEmpty() ? "当前没有可进入的目的地" : "点击目的地进入："));
                    for (JsonElement item : destinations) {
                        JsonObject destination = item.getAsJsonObject();
                        String dest = Protocol.text(destination, "dest");
                        String title = Protocol.text(destination, "title");
                        player.sendSystemMessage(Component.literal(title + " · " + dest)
                            .withStyle(style -> style.withClickEvent(new ClickEvent.RunCommand("/server " + dest))));
                    }
                });
            } catch (Exception error) {
                message(server, player, "读取目的地失败：" + safeMessage(error), false);
            }
        });
    }

    private static String safeMessage(Exception error) {
        if (error instanceof InterruptedException) return "操作已经取消";
        return error.getMessage() == null ? error.getClass().getSimpleName() : error.getMessage();
    }

    private void begin(MinecraftServer server, ServerPlayer player, String destination) {
        UUID uuid = player.getUUID();
        String name = player.getGameProfile().name();
        FutureTask<Void> task = new FutureTask<>(() -> { transfer(server, player, destination, uuid, name); return null; }) {
            @Override protected void done() { pending.remove(uuid, this); }
        };
        if (pending.putIfAbsent(uuid, task) != null) {
            player.sendSystemMessage(Component.literal("已有一次房间传送正在进行"));
            return;
        }
        player.sendSystemMessage(Component.literal("目标房间启动中…"), true);
        network.execute(task);
    }

    private static Duration remainingTimeout(long deadline) throws IOException {
        long remaining = deadline - System.nanoTime();
        if (remaining <= 0) throw new IOException("等待目标房间超过 300 秒");
        return Duration.ofNanos(Math.min(remaining, Duration.ofSeconds(10).toNanos()));
    }

    private JsonObject start(String destination, UUID uuid, String name, long deadline) throws IOException, InterruptedException {
        JsonObject body = new JsonObject();
        body.addProperty("fromRoom", roomID);
        body.addProperty("dest", destination);
        JsonObject identity = new JsonObject();
        identity.addProperty("uuid", uuid.toString());
        identity.addProperty("name", name);
        body.add("player", identity);
        return call("POST", projectPath("/hop"), body, remainingTimeout(deadline)).getAsJsonObject();
    }

    private void cancelLease(String room, String lease) {
        if (room == null || lease == null) return;
        http.sendAsync(request(projectPath("/rooms/" + segment(room) + "/leases/" + segment(lease))).DELETE().build(), HttpResponse.BodyHandlers.discarding())
            .whenComplete((response, error) -> {
                if (error != null || response.statusCode() != 204) LOG.warn("目标房间 {} 的传送租约取消失败", room);
            });
    }

    private void transfer(MinecraftServer server, ServerPlayer player, String destination, UUID uuid, String name) {
        String targetID = null;
        String leaseID = null;
        AtomicBoolean transferred = new AtomicBoolean();
        try {
            long deadline = System.nanoTime() + Duration.ofSeconds(300).toNanos();
            long renewAt = System.nanoTime() + Duration.ofSeconds(120).toNanos();
            JsonObject started = start(destination, uuid, name, deadline);
            JsonObject room = started.getAsJsonObject("room");
            targetID = Protocol.text(room, "id");
            leaseID = Protocol.text(started.getAsJsonObject("lease"), "id");
            long generation = Protocol.generation(room);
            while (true) {
                if (Thread.currentThread().isInterrupted()) throw new InterruptedException();
                if (System.nanoTime() >= deadline) throw new IOException("等待目标房间超过 300 秒");
                if (Protocol.ready(room, project, targetID, generation, buildID)) {
                    String host = Protocol.text(room, "host");
                    int port = room.get("port").getAsInt();
                    if (host.isBlank() || port < 1 || port > 65535) throw new IOException("目标地址无效");
                    FutureTask<Void> sent = new FutureTask<>(() -> {
                        if (System.nanoTime() >= deadline) throw new IOException("等待目标房间超过 300 秒");
                        if (player.hasDisconnected()) throw new IllegalStateException("玩家已经离开来源房间");
                        transferred.set(true);
                        player.connection.send(new ClientboundTransferPacket(host, port));
                        return null;
                    });
                    server.execute(sent);
                    try {
                        sent.get(Math.max(1, deadline - System.nanoTime()), TimeUnit.NANOSECONDS);
                    } finally {
                        sent.cancel(false);
                    }
                    return;
                }
                message(server, player, "目标房间启动中…（" + Protocol.text(room, "phase") + "）", true);
                Thread.sleep(Duration.ofSeconds(2));
                if (System.nanoTime() >= renewAt) {
                    JsonObject renewed = start(destination, uuid, name, deadline);
                    String oldLease = leaseID;
                    leaseID = Protocol.text(renewed.getAsJsonObject("lease"), "id");
                    cancelLease(targetID, oldLease);
                    room = renewed.getAsJsonObject("room");
                    renewAt = System.nanoTime() + Duration.ofSeconds(120).toNanos();
                } else {
                    room = call("GET", projectPath("/rooms/" + segment(targetID)), null, remainingTimeout(deadline)).getAsJsonObject();
                }
            }
        } catch (Exception error) {
            message(server, player, "传送取消：" + safeMessage(error), false);
        } finally {
            if (!transferred.get()) cancelLease(targetID, leaseID);
        }
    }
}

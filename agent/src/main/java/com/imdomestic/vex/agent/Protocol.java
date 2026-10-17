package com.imdomestic.vex.agent;

import com.google.gson.JsonObject;
import com.mojang.brigadier.arguments.StringArgumentType;
import com.mojang.brigadier.exceptions.CommandSyntaxException;
import com.mojang.brigadier.exceptions.SimpleCommandExceptionType;
import com.mojang.brigadier.LiteralMessage;
import java.util.Set;

final class Protocol {
    private static final Set<String> TERMINAL = Set.of("failed", "stopping", "archiving", "closed", "lost");

    static StringArgumentType destinationArgument() { return StringArgumentType.greedyString(); }

    static String destination(String value) throws CommandSyntaxException {
        if (!value.matches("(?:(?:world|room):)?[a-z0-9][a-z0-9-]{0,62}")) {
            throw new SimpleCommandExceptionType(new LiteralMessage("目的地必须是世界 ID、房间 ID、world:<ID> 或 room:<ID>")).create();
        }
        if (value.startsWith("world:") && value.length() > 22) {
            throw new SimpleCommandExceptionType(new LiteralMessage("世界 ID 最多 16 个字符")).create();
        }
        return value;
    }

    static String text(JsonObject object, String key) {
        if (!object.has(key) || !object.get(key).isJsonPrimitive() || !object.get(key).getAsJsonPrimitive().isString()) {
            throw new IllegalArgumentException("控制器响应缺少字符串字段 " + key);
        }
        return object.get(key).getAsString();
    }

    static long generation(JsonObject room) {
        return room.get("generation").getAsBigDecimal().longValueExact();
    }

    static boolean ready(JsonObject room, String project, String id, long generation, String build) {
        if (!text(room, "project").equals(project) || !text(room, "id").equals(id) || generation(room) != generation || !text(room, "buildId").equals(build)) {
            throw new IllegalStateException("目标房间的启动周期或构建已经改变");
        }
        String state = text(room, "state");
        if (TERMINAL.contains(state)) {
            String message = "目标房间状态为 " + state;
            if (room.has("lastError") && room.get("lastError").isJsonObject()) {
                message += "：" + text(room.getAsJsonObject("lastError"), "message");
            }
            throw new IllegalStateException(message);
        }
        return state.equals("ready");
    }
}

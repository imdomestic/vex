package com.imdomestic.chorus.hop;

import com.google.gson.JsonObject;
import com.mojang.brigadier.StringReader;
import com.mojang.brigadier.exceptions.CommandSyntaxException;
import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertFalse;

public final class ProtocolTest {
    @Test void parsesQualifiedDestinationsWithBrigadier() throws CommandSyntaxException {
        for (String value : new String[]{"plaza", "main-fabric", "asset:plaza", "room:main-fabric", "room:a-plaza-fc16eaac-fabric"}) {
            StringReader reader = new StringReader(value);
            assertEquals(value, Protocol.destination(Protocol.destinationArgument().parse(reader)));
            assertFalse(reader.canRead());
        }
        for (String value : new String[]{"", "asset:", "room:", "asset:plaza other", "other:plaza", "room:../x", "room:x\ny", "asset:abcdefghijklmnopq"}) {
            assertThrows(CommandSyntaxException.class, () -> Protocol.destination(Protocol.destinationArgument().parse(new StringReader(value))));
        }
    }

    @Test void requiresMatchingReadyLevel() {
        JsonObject room = new JsonObject();
        room.addProperty("id", "main-fabric");
        room.addProperty("generation", 3);
        room.addProperty("buildId", "fabric-fc16eaac252b");
        for (String state : new String[]{"queued", "building", "starting", "ready"}) {
            room.addProperty("state", state);
            assertEquals(state.equals("ready"), Protocol.ready(room, "main-fabric", 3, "fabric-fc16eaac252b"));
        }
        for (String state : new String[]{"failed", "stopping", "archiving", "closed", "lost"}) {
            room.addProperty("state", state);
            rejects(() -> Protocol.ready(room, "main-fabric", 3, "fabric-fc16eaac252b"));
        }
        room.addProperty("state", "ready");
        rejects(() -> Protocol.ready(room, "other-fabric", 3, "fabric-fc16eaac252b"));
        rejects(() -> Protocol.ready(room, "main-fabric", 4, "fabric-fc16eaac252b"));
        rejects(() -> Protocol.ready(room, "main-fabric", 3, "fabric-other"));
    }

    private static void rejects(Runnable check) {
        assertThrows(IllegalStateException.class, check::run);
    }
}

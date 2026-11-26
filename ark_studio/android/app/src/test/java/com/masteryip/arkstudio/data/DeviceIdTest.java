package com.masteryip.arkstudio.data;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotEquals;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

public class DeviceIdTest {
    @Test
    public void hashIsStableSaltedAndAcceptedByTheServer() {
        String a = DeviceId.hash("8f2c1d0e9b7a6c54");
        assertEquals(a, DeviceId.hash(" 8f2c1d0e9b7a6c54 "));
        assertNotEquals(a, DeviceId.hash("8f2c1d0e9b7a6c55"));
        assertFalse(a.contains("8f2c1d0e9b7a6c54"));
        assertTrue(a, a.matches("[0-9a-f]{64}"));
    }

    @Test
    public void brokenAndroidIdsAreNotUsed() {
        assertFalse(DeviceId.usable(null));
        assertFalse(DeviceId.usable(" "));
        assertFalse(DeviceId.usable("9774d56d682e549c"));
        assertTrue(DeviceId.usable("8f2c1d0e9b7a6c54"));
    }
}

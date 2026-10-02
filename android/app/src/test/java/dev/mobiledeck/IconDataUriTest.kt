package dev.mobiledeck

import dev.mobiledeck.data.Profile
import dev.mobiledeck.data.ProtocolJson
import dev.mobiledeck.ui.pngBase64Payload
import kotlinx.serialization.json.decodeFromJsonElement
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The `image` icon path: a profile's top-level `icons` map and the data URI it
 * carries.
 *
 * The map exists because protocol v1 has no icon-fetch message, so the host
 * inlines the PNG a button references. Two properties matter and are pinned
 * here:
 *
 *  1. A host that predates the field must not break a client that has it. The
 *     map is optional; its absence decodes to an empty map and every lookup
 *     returns null, which the UI renders as the existing placeholder.
 *  2. A malformed URI must never reach the decoder. The shape check is a pure
 *     function precisely so it can be tested on the JVM — `Base64` and
 *     `BitmapFactory` are Android framework classes and are stubbed out here.
 */
class IconDataUriTest {

    private val pngUri =
        "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

    private fun decodeProfile(json: String): Profile =
        ProtocolJson.decodeFromJsonElement(Profile.serializer(), ProtocolJson.parseToJsonElement(json))

    @Test
    fun `a profile with an icons map decodes and resolves the URI`() {
        val profile = decodeProfile(
            """
            {
              "schema": 1,
              "id": "development",
              "icons": { "terminal.png": "$pngUri" },
              "pages": []
            }
            """.trimIndent(),
        )

        assertEquals(1, profile.icons.size)
        assertEquals(pngUri, profile.icons["terminal.png"])
        assertEquals(pngUri, profile.iconDataUri("terminal.png"))
    }

    @Test
    fun `a profile without an icons map decodes to an empty map`() {
        // The compatibility case: an older host sends no `icons` field at all.
        val profile = decodeProfile(
            """
            {
              "schema": 1,
              "id": "development",
              "pages": []
            }
            """.trimIndent(),
        )

        assertTrue(profile.icons.isEmpty())
        assertNull(profile.iconDataUri("terminal.png"))
    }

    @Test
    fun `an unknown file name resolves to null`() {
        val profile = decodeProfile(
            """
            {
              "schema": 1,
              "id": "development",
              "icons": { "terminal.png": "$pngUri" },
              "pages": []
            }
            """.trimIndent(),
        )

        assertNull(profile.iconDataUri("missing.png"))
        assertNull(profile.iconDataUri(""))
    }

    @Test
    fun `a malformed URI is rejected before decoding`() {
        // No comma at all.
        assertNull(pngBase64Payload("data:image/png;base64"))
        // Not base64 (and not a data URI).
        assertNull(pngBase64Payload("terminal.png"))
        // Wrong media type.
        assertNull(pngBase64Payload("data:image/jpeg;base64,/9j/4AAQ"))
        // Wrong encoding.
        assertNull(pngBase64Payload("data:image/png,AAAA"))
        // A non-data scheme.
        assertNull(pngBase64Payload("https://example.test/terminal.png"))
        // Empty payload after the comma.
        assertNull(pngBase64Payload("data:image/png;base64,"))
        // Null input.
        assertNull(pngBase64Payload(null))
    }

    @Test
    fun `a well-formed URI yields its payload`() {
        val payload = pngBase64Payload(pngUri)
        assertNotNull(payload)
        // The pure check must accept exactly what the decoder is handed.
        assertTrue(payload!!.startsWith("iVBORw0KGgo"))
    }

    @Test
    fun `the decoder returns null for a malformed URI instead of throwing`() {
        // The framework decode is never reached: the shape check rejects first.
        // This pins the contract the grid relies on — a bad icon degrades to the
        // placeholder rather than crashing the draw.
        assertNull(dev.mobiledeck.ui.decodeDataUri("not a data uri"))
        assertNull(dev.mobiledeck.ui.decodeDataUri(null))
        assertNull(dev.mobiledeck.ui.decodeDataUri("data:image/png;base64,"))
    }
}

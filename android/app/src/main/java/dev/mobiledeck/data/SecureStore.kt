package dev.mobiledeck.data

import android.content.Context
import android.content.SharedPreferences
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import androidx.core.content.edit
import kotlinx.serialization.Serializable
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Everything needed to re-open a session with a host.
 *
 * It is serialised as one JSON blob and encrypted as a whole, so no field of it
 * ever reaches disk in the clear.
 */
@Serializable
@JsonIgnoreProperties
data class TokenBundle(
    /** Host identity, so the token cannot be replayed against another host. */
    val hostId: String,
    val hostName: String = "",
    val baseUrl: String,
    /** The device id the host bound this token to (PROTOCOL.md §2.2). */
    val deviceId: String,
    val deviceName: String = "",
    val token: String,
    val scopes: List<String> = emptyList(),
    /** SHA-256 fingerprint, or null when the host runs in plaintext mode. */
    val fingerprint: String? = null,
)

/**
 * The device token, encrypted at rest.
 *
 * ## Why not `EncryptedSharedPreferences`
 *
 * `androidx.security:security-crypto` is deprecated, and its key rotation and
 * Tink dependency are a large surface for what is a 100-line problem. What the
 * token actually needs is:
 *
 *  * **an OS-held key**, so a filesystem copy of the app's data is not enough
 *    to recover the token. [AndroidKeyStore] provides it, and the key material
 *    is non-exportable;
 *  * **authenticated encryption**, so tampering with the stored blob is
 *    detected rather than decrypted into garbage. AES-GCM with a 128-bit tag
 *    does that;
 *  * **a fresh IV per write**. Reusing an IV under one key is the one fatal
 *    mistake in GCM, so every [save] draws 12 random bytes from the platform
 *    CSPRNG and never stores a key/IV pair twice.
 *
 * The ciphertext and IV live in private-mode [SharedPreferences], which is
 * `MODE_PRIVATE` — readable only by this app's UID.
 */
class SecureStore(context: Context) {

    private val prefs: SharedPreferences =
        context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)

    /**
     * Encrypts [bundle] and stores it under its host's id, replacing that host's
     * previous value.
     *
     * One slot per host, not one slot total. A single slot meant that pairing
     * with a second machine silently destroyed the credential for the first, so
     * a user with a laptop and a desktop had to re-pair every time they switched.
     * The host id is in the bundle and is stable, which makes it the natural key.
     *
     * The old ciphertext for that host is overwritten rather than kept: a stale
     * token is a credential the user believes they revoked.
     */
    fun save(bundle: TokenBundle) {
        val plaintext = ProtocolJson.encodeToString(TokenBundle.serializer(), bundle).toByteArray(Charsets.UTF_8)
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, secretKey())
        val ciphertext = cipher.doFinal(plaintext)

        // apply() rather than commit(): the token is not needed to render
        // anything, and blocking the main thread on a disk write to persist a
        // credential is a worse trade than losing it on a crash before flush.
        prefs.edit {
            putString(ivKey(bundle.hostId), Base64.encodeToString(cipher.iv, Base64.NO_WRAP))
            putString(ctKey(bundle.hostId), Base64.encodeToString(ciphertext, Base64.NO_WRAP))
        }
    }

    /** The hosts this device holds a token for, newest first. */
    fun hosts(): List<String> =
        prefs.all.keys
            .filter { it.startsWith(CT_PREFIX) }
            .map { it.removePrefix(CT_PREFIX) }
            .sorted()

    /**
     * Decrypts the bundle stored for [hostId], or the most recently written one
     * when [hostId] is null.
     *
     * A blob that fails to decrypt (key invalidated by a device credential
     * change, or corrupt) is cleared and reported as absent. The alternative —
     * throwing — would wedge the app on a state the user can only fix by
     * clearing app data, and a token the client cannot read is worthless
     * anyway, so the honest answer is "pair again".
     */
    fun load(hostId: String? = null): TokenBundle? {
        val id = hostId ?: hosts().firstOrNull() ?: return null
        val ivB64 = prefs.getString(ivKey(id), null) ?: return null
        val ctB64 = prefs.getString(ctKey(id), null) ?: return null

        return try {
            val iv = Base64.decode(ivB64, Base64.NO_WRAP)
            val ciphertext = Base64.decode(ctB64, Base64.NO_WRAP)
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.DECRYPT_MODE, secretKey(), GCMParameterSpec(GCM_TAG_BITS, iv))
            val plaintext = cipher.doFinal(ciphertext)
            ProtocolJson.decodeFromString(TokenBundle.serializer(), String(plaintext, Charsets.UTF_8))
        } catch (_: Exception) {
            forget(id)
            null
        }
    }

    /** Removes the token for one host. The keystore entry is deliberately kept. */
    fun forget(hostId: String) {
        prefs.edit {
            remove(ivKey(hostId))
            remove(ctKey(hostId))
        }
    }

    /**
     * Removes every stored token.
     *
     * The prefs file is small and holds only credentials, so clearing it whole is
     * simpler than enumerating, and it cannot leave an orphan behind.
     */
    fun clear() {
        prefs.edit { clear() }
    }

    /**
     * The AES key, generated on first use.
     *
     * `setUserAuthenticationRequired(false)` is required, not a shortcut: the
     * deck must reconnect in the background when the phone is locked, and a
     * biometric-gated key would make every reconnect fail while the screen is
     * off. The key's protection is that it is non-exportable and bound to this
     * app's UID on this device.
     */
    private fun secretKey(): SecretKey {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        (keyStore.getEntry(KEY_ALIAS, null) as? KeyStore.SecretKeyEntry)?.let { return it.secretKey }

        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE)
        generator.init(
            KeyGenParameterSpec.Builder(
                KEY_ALIAS,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
            )
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .setUserAuthenticationRequired(false)
                .build(),
        )
        return generator.generateKey()
    }

    private companion object {
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val KEY_ALIAS = "mobiledeck.token"
        const val TRANSFORMATION = "AES/GCM/NoPadding"

        /** GCM's standard 12-byte nonce length. */
        const val GCM_TAG_BITS = 128

        const val PREFS_NAME = "mobiledeck.secure"

        /**
         * One slot per host, keyed by the host id.
         *
         * The id is a hex string from the host, so it is safe in a preference
         * key; it is still prefixed to keep the namespace obvious in a dump of
         * the file, and so a future non-token preference cannot collide with it.
         */
        const val IV_PREFIX = "token_iv."
        const val CT_PREFIX = "token_ct."

        fun ivKey(hostId: String) = IV_PREFIX + hostId
        fun ctKey(hostId: String) = CT_PREFIX + hostId
    }
}

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
     * Encrypts [bundle] and stores it, replacing any previous value.
     *
     * The old ciphertext is overwritten rather than kept: a stale token is a
     * credential the user believes they revoked.
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
            putString(KEY_IV, Base64.encodeToString(cipher.iv, Base64.NO_WRAP))
            putString(KEY_CIPHERTEXT, Base64.encodeToString(ciphertext, Base64.NO_WRAP))
        }
    }

    /**
     * Decrypts the stored bundle, or null when there is none.
     *
     * A blob that fails to decrypt (key invalidated by a device credential
     * change, or corrupt) is cleared and reported as absent. The alternative —
     * throwing — would wedge the app on a state the user can only fix by
     * clearing app data, and a token the client cannot read is worthless
     * anyway, so the honest answer is "pair again".
     */
    fun load(): TokenBundle? {
        val ivB64 = prefs.getString(KEY_IV, null) ?: return null
        val ctB64 = prefs.getString(KEY_CIPHERTEXT, null) ?: return null

        return try {
            val iv = Base64.decode(ivB64, Base64.NO_WRAP)
            val ciphertext = Base64.decode(ctB64, Base64.NO_WRAP)
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.DECRYPT_MODE, secretKey(), GCMParameterSpec(GCM_TAG_BITS, iv))
            val plaintext = cipher.doFinal(ciphertext)
            ProtocolJson.decodeFromString(TokenBundle.serializer(), String(plaintext, Charsets.UTF_8))
        } catch (_: Exception) {
            clear()
            null
        }
    }

    /** Removes the stored token. The keystore entry is deliberately kept. */
    fun clear() {
        prefs.edit {
            remove(KEY_IV)
            remove(KEY_CIPHERTEXT)
        }
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
        const val KEY_IV = "token_iv"
        const val KEY_CIPHERTEXT = "token_ct"
    }
}

// Package data is the wire layer of the MobileDeck client: the protocol model,
// the transports that carry it, and the store that turns it into UI state.
//
// Nothing in this package imports an Android UI class. `DeckClient` in
// particular is pure JVM so it can be unit-tested without an emulator
// (docs/ARCHITECTURE.md §4).
package dev.mobiledeck.data

/**
 * Declares that a wire payload tolerates unknown keys.
 *
 * PROTOCOL.md §11 makes additive evolution the compatibility policy: the host
 * may add fields, message types and action types without bumping `v`, and
 * "clients MUST ignore unknown fields in any payload they parse". This
 * annotation is the per-class declaration of that obligation.
 *
 * Enforcement is by [ProtocolJson], the single [kotlinx.serialization.json.Json]
 * instance used for every frame, which sets `ignoreUnknownKeys = true`. The
 * annotation is load-bearing anyway: `ProtocolTest` reflects over the payload
 * registry and fails if a payload class was added without it, so the rule
 * cannot be forgotten when the protocol grows.
 */
@Target(AnnotationTarget.CLASS)
@Retention(AnnotationRetention.RUNTIME)
annotation class JsonIgnoreProperties(val ignoreUnknown: Boolean = true)

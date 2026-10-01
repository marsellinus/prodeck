# MobileDeck keeps the protocol data classes reflective-free by using
# kotlinx.serialization, so no model class needs a keep rule. The rules below
# only cover OkHttp/Okio, which use reflection for platform detection.
-dontwarn okhttp3.**
-dontwarn okio.**
-dontwarn org.conscrypt.**
-dontwarn org.bouncycastle.**
-dontwarn org.openjsse.**

# Kotlin serialization runtime lookups.
-keepattributes *Annotation*, InnerClasses
-dontnote kotlinx.serialization.**
-keepclassmembers class kotlinx.serialization.json.** {
    *** Companion;
}
-keepclasseswithmembers class kotlinx.serialization.json.** {
    kotlinx.serialization.KSerializer serializer(...);
}
-keep,includedescriptorclasses class dev.mobiledeck.**$$serializer { *; }
-keepclassmembers class dev.mobiledeck.** {
    *** Companion;
}
-keepclasseswithmembers class dev.mobiledeck.** {
    kotlinx.serialization.KSerializer serializer(...);
}

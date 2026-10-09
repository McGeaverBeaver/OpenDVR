APKs placed here are baked into the container image and served at /downloads/<name>.apk.
CI drops the Android TV build in as opendvr-tv.apk before building the image.
The server re-signs these with its own key (DATA_DIR/apk-signing.pem) when it serves
them, so each new image's app installs over the previous one.

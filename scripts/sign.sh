#!/bin/sh
# Ad-hoc signs the built app with a stable identifier and a designated
# requirement on it alone (not the cdhash), so macOS can recognize later
# builds as the same app. On macOS: codesign; elsewhere: rcodesign.
set -e
app="${1:-build/darwin-universal/DS Search.app}"
id=dev.horizon.dssearch
if command -v codesign >/dev/null 2>&1 && [ "$(uname)" = Darwin ]; then
	codesign --force --deep -s - -i "$id" -r="designated => identifier \"$id\"" "$app"
	codesign -dr - "$app"
else
	dr=$(mktemp)
	python3 - "$dr" "$id" <<'PY'
import struct, sys
s = sys.argv[2].encode()
body = struct.pack('>III', 1, 2, len(s)) + s + b'\0' * (-len(s) % 4)
open(sys.argv[1], 'wb').write(struct.pack('>II', 0xfade0c00, 8 + len(body)) + body)
PY
	rcodesign sign --code-requirements-file "$dr" "$app"
	rm -f "$dr"
fi

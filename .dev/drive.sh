#!/bin/sh
# Usage: .dev/drive.sh '<js>' [shot-name]  — runs JS in the app window on the host.
shot=""; [ -n "$2" ] && shot="D:\\AI\\claude-docker\\workspace\\publisher\\.dev\\$2.png"
js=$(printf '%s' "$1" | base64 -w0)
host-exec "\$env:Path=\"\$env:LOCALAPPDATA\\Programs\\go\\bin;\$env:Path\"; Set-Location -LiteralPath 'D:\\AI\\claude-docker\\workspace\\publisher'; go run ./.dev/drive '$js' '$shot' 2>&1 | Out-String" 2>&1 | grep -v -e CLIXML -e '<Objs' -e '^<Obj'

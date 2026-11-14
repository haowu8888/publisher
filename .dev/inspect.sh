#!/bin/sh
# Usage: .dev/inspect.sh <url|tab substring> '<js>' [shot-name]  — see .dev/inspect/main.go.
shot=""; [ -n "$3" ] && shot="D:\\AI\\claude-docker\\workspace\\publisher\\.dev\\$3.png"
js=$(printf '%s' "$2" | base64 -w0)
host-exec "\$env:Path=\"\$env:LOCALAPPDATA\\Programs\\go\\bin;\$env:Path\"; Set-Location -LiteralPath 'D:\\AI\\claude-docker\\workspace\\publisher'; go run ./.dev/inspect '$1' '$js' '$shot' 2>&1 | Out-String -Width 400" 2>&1 | grep -v -e CLIXML -e '<Objs' -e '^<Obj'

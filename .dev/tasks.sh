#!/bin/sh
# Usage: .dev/tasks.sh [n]  — prints the newest n tasks (default 4) from the app's data.json.
# The file's steps may be old: a task's step alone is not written to it. Statuses are current.
host-exec "[Console]::OutputEncoding=[Text.Encoding]::UTF8; (Get-Content -Raw -Encoding UTF8 \"\$env:APPDATA\\Publisher\\data.json\" | ConvertFrom-Json).tasks | select -first ${1:-4} | ft platform,mode,status,step,error,updatedAt -auto -wrap | Out-String -Width 250" 2>&1 | grep -v -e CLIXML -e '<Obj' -e '^\s*$'

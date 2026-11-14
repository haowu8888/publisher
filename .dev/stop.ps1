# Stops the app and its automation Chrome (only the one on the app's profile).
Stop-Process -Name Publisher -Force -ErrorAction SilentlyContinue
Get-CimInstance Win32_Process | Where-Object { $_.Name -eq 'chrome.exe' -and $_.CommandLine -like '*Publisher\chrome-profile*' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
Start-Sleep 2
"left: " + @(Get-CimInstance Win32_Process | Where-Object { $_.Name -eq 'chrome.exe' -and $_.CommandLine -like '*Publisher\chrome-profile*' }).Count
"publisher: " + @(Get-Process Publisher -ErrorAction SilentlyContinue).Count

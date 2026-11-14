# Posts a harmless test cookie to the running app's sync server, as the extension would.
$exp = [DateTimeOffset]::UtcNow.AddMinutes(2).ToUnixTimeSeconds()
$body = @{ cookies = @(
  @{ name = "publisher_sync_test"; value = "1"; domain = ".bilibili.com"; path = "/"; hostOnly = $false; secure = $true; httpOnly = $false; session = $false; sameSite = "lax"; expirationDate = $exp },
  @{ name = "evil"; value = "1"; domain = ".example.com"; path = "/" }
) } | ConvertTo-Json -Depth 4
$bytes = [Text.Encoding]::UTF8.GetBytes($body)
try {
  Invoke-WebRequest http://127.0.0.1:17891/import -Method Post -Body $bytes -ContentType "application/json" -Headers @{ Origin = "https://evil.com" } -UseBasicParsing | Out-Null
  "web page origin: accepted (BAD)"
} catch { "web page origin: " + $_.Exception.Response.StatusCode.value__ }
$r = Invoke-WebRequest http://127.0.0.1:17891/import -Method Post -Body $bytes -ContentType "application/json" -Headers @{ Origin = "chrome-extension://test" } -UseBasicParsing
"extension origin: " + [Text.Encoding]::UTF8.GetString($r.RawContentStream.ToArray())

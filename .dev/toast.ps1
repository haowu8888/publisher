# Captures the corner of the screen where Windows shows notifications, to
# .dev/toast1.png … toast3.png over a few seconds. Run through an
# interactive scheduled task (PublisherShot), since the SSH session has no
# desktop.
Add-Type -AssemblyName System.Drawing, System.Windows.Forms
$b = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
$w = [Math]::Min(640, $b.Width); $h = [Math]::Min(420, $b.Height)
foreach ($i in 1..3) {
  Start-Sleep -Milliseconds 1800
  $bmp = New-Object System.Drawing.Bitmap $w, $h
  $g = [System.Drawing.Graphics]::FromImage($bmp)
  $g.CopyFromScreen($b.Right - $w, $b.Bottom - $h, 0, 0, $bmp.Size)
  $bmp.Save("$PSScriptRoot\toast$i.png")
  $g.Dispose(); $bmp.Dispose()
}

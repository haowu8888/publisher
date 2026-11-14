# Captures the window whose title contains $env:SHOT_TITLE (default: the
# automation Chrome's active tab) to .dev/screen.png, even when covered.
# Run through the interactive scheduled task PublisherShot, since the SSH
# session has no desktop.
param([string]$Title = "Chrome")
Add-Type -AssemblyName System.Drawing
Add-Type @"
using System; using System.Runtime.InteropServices; using System.Text;
public class W {
  public delegate bool EnumProc(IntPtr h, IntPtr p);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumProc f, IntPtr p);
  [DllImport("user32.dll")] public static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr h, IntPtr dc, uint f);
  public struct RECT { public int L, T, R, B; }
}
"@
$found = [IntPtr]::Zero
[W]::EnumWindows({ param($h, $p)
  $s = New-Object System.Text.StringBuilder 512
  [void][W]::GetWindowText($h, $s, 512)
  if ([W]::IsWindowVisible($h) -and $s.ToString().Contains($Title)) { $script:found = $h; return $false }
  return $true }, [IntPtr]::Zero) | Out-Null
if ($found -eq [IntPtr]::Zero) { "no window: $Title" | Out-File "$PSScriptRoot\screen.txt"; exit 1 }
$r = New-Object W+RECT
[void][W]::GetWindowRect($found, [ref]$r)
$bmp = New-Object System.Drawing.Bitmap ($r.R - $r.L), ($r.B - $r.T)
$g = [System.Drawing.Graphics]::FromImage($bmp)
$dc = $g.GetHdc()
[void][W]::PrintWindow($found, $dc, 2)
$g.ReleaseHdc($dc)
$bmp.Save("$PSScriptRoot\screen.png")
"ok" | Out-File "$PSScriptRoot\screen.txt"

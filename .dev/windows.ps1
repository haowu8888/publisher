Add-Type @"
using System; using System.Runtime.InteropServices; using System.Text;
public class W2 {
  public delegate bool EnumProc(IntPtr h, IntPtr p);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumProc f, IntPtr p);
  [DllImport("user32.dll")] public static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
}
"@
$out = @()
[W2]::EnumWindows({ param($h, $p)
  $s = New-Object System.Text.StringBuilder 512
  [void][W2]::GetWindowText($h, $s, 512)
  if ([W2]::IsWindowVisible($h) -and $s.Length) { $script:out += $s.ToString() }
  return $true }, [IntPtr]::Zero) | Out-Null
$out | Out-File -Encoding UTF8 "$PSScriptRoot\screen.txt"

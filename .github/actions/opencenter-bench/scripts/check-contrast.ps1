# Render the served page in both colour schemes and measure real contrast.
#
# Every other check in this repository runs the page's JavaScript against a
# fake DOM. That proves the right elements exist and says nothing about
# whether a person can read them - the Run button on every row was green text
# on pale green at 1.8:1, and every check passed.
#
#   powershell -File scripts/check-contrast.ps1 [-Port 7700]
#
# Needs Chrome, which lives on the Windows side of WSL.
#
# ASCII only, deliberately: Windows PowerShell 5.1 reads a .ps1 without a BOM
# as ANSI, so a single em-dash here is a parse error.

param([int]$Port = 7700)

Add-Type -AssemblyName System.Drawing

$chrome = "C:\Program Files\Google\Chrome\Application\chrome.exe"
if (-not (Test-Path $chrome)) { Write-Host "  SKIP  Chrome not found at $chrome"; exit 0 }

$dir = Join-Path $env:TEMP "opencli-contrast"
New-Item -ItemType Directory -Force -Path $dir | Out-Null

# Relative luminance, then the WCAG ratio.
function Get-Luminance($c) {
  $v = @($c.R, $c.G, $c.B) | ForEach-Object {
    $s = $_ / 255.0
    if ($s -le 0.03928) { $s / 12.92 } else { [Math]::Pow((($s + 0.055) / 1.055), 2.4) }
  }
  0.2126 * $v[0] + 0.7152 * $v[1] + 0.0722 * $v[2]
}

function Get-Contrast($a, $b) {
  $x = Get-Luminance $a
  $y = Get-Luminance $b
  if ($x -lt $y) { $t = $x; $x = $y; $y = $t }
  [Math]::Round((($x + 0.05) / ($y + 0.05)), 2)
}

# The darkest and lightest pixel in a region: for small text on a tint those
# are the glyph and its background, which is the pair that matters.
function Get-Extremes($bmp, $x, $y, $w, $h) {
  $dark = $null; $light = $null; $dl = 2.0; $ll = -1.0
  for ($i = $x; $i -lt ($x + $w); $i++) {
    for ($j = $y; $j -lt ($y + $h); $j++) {
      if ($i -ge $bmp.Width -or $j -ge $bmp.Height) { continue }
      $p = $bmp.GetPixel($i, $j)
      $l = Get-Luminance $p
      if ($l -lt $dl) { $dl = $l; $dark = $p }
      if ($l -gt $ll) { $ll = $l; $light = $p }
    }
  }
  @{ Dark = $dark; Light = $light }
}

# Regions holding small coloured text on a tinted background - the pattern
# that failed. Coordinates follow the 1700px layout.
#
# A filled control has to be sampled INSIDE its fill. Taking the whole button
# compared the page background against the fill and reported 3.5:1 for a label
# that was actually 2.2:1 against the thing it sat on.
# The stage rail moved from a row across the top to a column on the left, and
# its steps are now solid iOS fills rather than tints, so each one is sampled:
# five of those fills are bright enough that the wrong text colour on them
# would be unreadable, and only measuring says which.
$regions = @(
  # Located by finding the accent fill in the render rather than guessed: the
  # old coordinate was left behind by a layout change and quietly measured
  # empty background at 1:1, which reads as a catastrophic failure and is
  # actually a stale number.
  @{ Name = "Run button";    X = 1632; Y = 909; W = 34; H = 8 },
  @{ Name = "strip heading"; X = 33;   Y = 60;  W = 90; H = 12 },
  @{ Name = "step 1 init";      X = 40; Y = 445; W = 46; H = 11 },
  @{ Name = "step 2 configure"; X = 40; Y = 505; W = 74; H = 11 },
  @{ Name = "step 3 validate";  X = 40; Y = 578; W = 66; H = 11 },
  @{ Name = "step 4 generate";  X = 40; Y = 652; W = 70; H = 11 },
  @{ Name = "step 5 deploy";    X = 40; Y = 725; W = 60; H = 11 },
  @{ Name = "step 6 operate";   X = 40; Y = 798; W = 62; H = 11 },
  @{ Name = "step 7 teardown";  X = 40; Y = 870; W = 72; H = 11 }
)

$failures = 0
foreach ($scheme in @("light", "dark")) {
  $shot = Join-Path $dir "$scheme.png"
  if (Test-Path $shot) { Remove-Item $shot }

  # ?theme= selects it. The page no longer follows the system, so
  # --force-dark-mode would render the same thing twice and prove nothing.
  $flags = @("--headless=new", "--disable-gpu", "--hide-scrollbars",
    "--virtual-time-budget=6000", "--window-size=1700,1150",
    "--screenshot=$shot", "http://127.0.0.1:$Port/?theme=$scheme")
  & $chrome $flags 2>&1 | Out-Null

  if (-not (Test-Path $shot)) {
    Write-Host "  FAIL  no screenshot for $scheme"
    $failures++
    continue
  }

  $bmp = [System.Drawing.Bitmap]::FromFile($shot)
  foreach ($r in $regions) {
    $e = Get-Extremes $bmp $r.X $r.Y $r.W $r.H
    if (-not $e.Dark -or -not $e.Light) { continue }
    $ratio = Get-Contrast $e.Dark $e.Light
    # 4.5:1 is the floor for body text. These are small and bold, and the
    # failure being guarded against measured 1.8, so 3.0 is the alarm line.
    if ($ratio -ge 3.0) {
      Write-Host ("  ok    {0,-14} {1,-6} {2}:1" -f $r.Name, $scheme, $ratio)
    } else {
      Write-Host ("  FAIL  {0,-14} {1,-6} {2}:1 unreadable" -f $r.Name, $scheme, $ratio)
      $failures++
    }
  }
  $bmp.Dispose()
}

Write-Host ""
if ($failures -eq 0) {
  Write-Host "  every measured region is readable in both schemes"
} else {
  Write-Host "  $failures region(s) below the threshold"
  exit 1
}

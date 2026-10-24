$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Write-Host "PS" $PSVersionTable.PSVersion

$dir = Join-Path $env:TEMP 'winget-install'
New-Item -ItemType Directory -Force -Path $dir | Out-Null

Write-Host 'Downloading VCLibs...'
$vclibs = Join-Path $dir 'VCLibs.appx'
Invoke-WebRequest -Uri 'https://aka.ms/Microsoft.VCLibs.x64.14.00.Desktop.appx' -OutFile $vclibs -UseBasicParsing

Write-Host 'Downloading UI.Xaml 2.8 nuget...'
$xamlZip = Join-Path $dir 'xaml.zip'
Invoke-WebRequest -Uri 'https://www.nuget.org/api/v2/package/Microsoft.UI.Xaml/2.8.6' -OutFile $xamlZip -UseBasicParsing
$xamlDir = Join-Path $dir 'xaml'
if (Test-Path $xamlDir) { Remove-Item $xamlDir -Recurse -Force }
Expand-Archive $xamlZip -DestinationPath $xamlDir -Force
$xaml = Get-ChildItem -Path $xamlDir -Recurse -Filter 'Microsoft.UI.Xaml*.appx' |
  Where-Object { $_.FullName -match '\\x64\\' } |
  Select-Object -First 1
if (-not $xaml) {
  $xaml = Get-ChildItem -Path $xamlDir -Recurse -Filter '*.appx' | Select-Object -First 1
}
Write-Host "Xaml package:" $xaml.FullName

Write-Host 'Downloading winget msixbundle...'
$winget = Join-Path $dir 'winget.msixbundle'
Invoke-WebRequest -Uri 'https://github.com/microsoft/winget-cli/releases/download/v1.9.25200/Microsoft.DesktopAppInstaller_8wekyb3d8bbwe.msixbundle' -OutFile $winget -UseBasicParsing

Write-Host 'Installing packages...'
Add-AppxPackage -Path $vclibs -ErrorAction SilentlyContinue
if ($xaml) { Add-AppxPackage -Path $xaml.FullName -ErrorAction SilentlyContinue }
Add-AppxPackage -Path $winget

$wingetExe = Get-ChildItem "$env:LOCALAPPDATA\Microsoft\WindowsApps\winget.exe" -ErrorAction SilentlyContinue
if (-not $wingetExe) {
  $wingetExe = Get-ChildItem "$env:ProgramFiles\WindowsApps\Microsoft.DesktopAppInstaller*\winget.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
}
Write-Host "winget path:" $wingetExe.FullName
& $wingetExe.FullName --version

#Requires -Version 5
<#
  AISR 守护进程安装脚本(Windows 10 / 11)。

  以「当前登录用户」身份、在登录时后台自动启动 AISR daemon —— 必须是用户身份,
  才能用到你 claude / cursor 的登录态。不需要管理员权限,不用装额外工具(只用
  系统自带的任务计划程序)。

  用法(在本文件夹里开 PowerShell):
    powershell -ExecutionPolicy Bypass -File .\install-aisr.ps1

  可选参数:
    -Token    <str>   固定 token(默认随机生成一个)
    -Listen   <addr>  监听地址(默认 0.0.0.0:7878;只想本机可用: 127.0.0.1:7878)
    -LogLevel <str>   日志级别 info(默认)或 debug(排错时用,打每轮 CLI 拉起 + 事件)
#>
param(
  [string]$Token      = ([guid]::NewGuid().ToString("N")),
  [string]$Listen     = "0.0.0.0:7878",
  [string]$LogLevel   = "info",
  [string]$InstallDir = "$env:LOCALAPPDATA\AISR"
)
$ErrorActionPreference = "Stop"

# 0. 前提检查(只警告,不阻断)——AISR 复用这些 CLI 的登录态。
foreach ($cli in @("claude", "cursor-agent")) {
  if (-not (Get-Command $cli -ErrorAction SilentlyContinue)) {
    Write-Warning "'$cli' 不在 PATH。装好并登录后 AISR 才能用它;命令名不同的话,装完在 $InstallDir\run-aisr.ps1 里设 AISR_CLAUDE_BIN / AISR_CURSOR_BIN 指全路径。"
  }
}

# 1. 安装目录 + 拷贝二进制(同目录的 aisr.exe / aisr-windows-amd64.exe)。
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$exeSrc = Join-Path $PSScriptRoot "aisr.exe"
if (-not (Test-Path $exeSrc)) { $exeSrc = Join-Path $PSScriptRoot "aisr-windows-amd64.exe" }
if (-not (Test-Path $exeSrc)) { throw "找不到 aisr.exe(应与本脚本在同一文件夹)。" }
Copy-Item $exeSrc (Join-Path $InstallDir "aisr.exe") -Force

# 2. 启动器(token 内置,用户本地文件)。计划任务运行它。
#    隐藏窗口跑,看不到控制台,所以用 --log-file 把日志落到 aisr.log(排错就看它)。
$runner = Join-Path $InstallDir "run-aisr.ps1"
@"
# AISR 启动器 —— 由计划任务在登录时隐藏窗口运行。
`$env:AISR_TOKEN = '$Token'
# 若 CLI 命令名不同,取消注释并填对:
# `$env:AISR_CLAUDE_BIN = 'claude.cmd'
# `$env:AISR_CURSOR_BIN = 'cursor-agent.cmd'
& "$InstallDir\aisr.exe" serve --listen $Listen --log-file "$InstallDir\aisr.log" --log-level $LogLevel
"@ | Set-Content -Encoding UTF8 $runner

# 3. 注册计划任务:登录时启动、当前用户交互式(能拿到登录态)、崩溃自动重启、不限时长。
$action = New-ScheduledTaskAction -Execute "powershell.exe" `
  -Argument "-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File `"$runner`""
$trigger = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME"
$principal = New-ScheduledTaskPrincipal -UserId "$env:USERDOMAIN\$env:USERNAME" -LogonType Interactive -RunLevel Limited
$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -AllowStartIfOnBatteries `
  -DontStopIfGoingOnBatteries -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
  -ExecutionTimeLimit ([TimeSpan]::Zero)
Register-ScheduledTask -TaskName "AISR Daemon" -Action $action -Trigger $trigger `
  -Principal $principal -Settings $settings -Description "AISR local runtime daemon" -Force | Out-Null

# 4. 立即启动。
Start-ScheduledTask -TaskName "AISR Daemon"
Start-Sleep -Seconds 2

Write-Host ""
Write-Host "AISR 已安装并启动(以后登录时自动运行)。" -ForegroundColor Green
Write-Host "  安装目录 : $InstallDir"
Write-Host "  Token    : $Token"
Write-Host "  监听     : $Listen"
Write-Host "  日志     : $InstallDir\aisr.log  (排错先看它;想更细: 重装时加 -LogLevel debug)"
Write-Host ""
Write-Host "  验证(本机):" -ForegroundColor Cyan
Write-Host "    curl -H `"Authorization: Bearer $Token`" http://127.0.0.1:7878/v1/providers"
Write-Host ""
Write-Host "  客户端(别的机器 / 容器)用:" -ForegroundColor Cyan
Write-Host "    AISR_BASE_URL = http://<这台机器的IP>:7878"
Write-Host "    AISR_TOKEN    = $Token"
Write-Host ""
Write-Host "  卸载: powershell -ExecutionPolicy Bypass -File .\uninstall-aisr.ps1"

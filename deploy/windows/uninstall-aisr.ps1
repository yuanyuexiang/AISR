#Requires -Version 5
# 卸载 AISR 守护进程:停任务 + 删任务 + 杀进程 + 删安装目录。
$ErrorActionPreference = "SilentlyContinue"

Stop-ScheduledTask       -TaskName "AISR Daemon"
Unregister-ScheduledTask -TaskName "AISR Daemon" -Confirm:$false
Get-Process aisr | Stop-Process -Force
Remove-Item "$env:LOCALAPPDATA\AISR" -Recurse -Force

Write-Host "AISR 已卸载(计划任务 + 进程 + 安装目录)。" -ForegroundColor Green

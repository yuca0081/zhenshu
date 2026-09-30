# 看门狗：server.exe 不在就拉起（隐藏窗口、无重定向；事实源是 manifest.jsonl）。
# 单实例保证：启动前先杀同类（按命令行匹配）。
$me = "watchdog_selfvideo"
Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" |
    Where-Object { $_.CommandLine -match $me -and $_.ProcessId -ne $PID } |
    ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }

$exe = "E:\workspace\develop\webpage-video-agent\bin\server.exe"
$dir = "E:\workspace\develop\webpage-video-agent"
$deadline = (Get-Date).AddHours(6)
while ((Get-Date) -lt $deadline) {
    $alive = Get-Process server -ErrorAction SilentlyContinue
    if (-not $alive) {
        Start-Process -FilePath $exe -WorkingDirectory "$dir\server" -WindowStyle Hidden
    }
    Start-Sleep -Seconds 15
}

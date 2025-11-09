@echo off
chcp 65001 >nul
echo 正在删除decision_logs文件夹中包含"获取持仓失败"的JSON文件...
echo.

set "target_path=decision_logs"

if not exist "%target_path%" (
    echo 错误: 当前目录下找不到 %target_path% 文件夹
    echo 当前目录: %cd%
    pause
    exit /b 1
)

echo 正在执行删除操作...
powershell -Command "Get-ChildItem -Path '%target_path%' -Recurse -Filter '*.json' | Where-Object { Select-String -Path $_.FullName -Pattern '获取持仓失败' -Quiet } | Remove-Item -Force"

echo.
echo 操作完成！
pause
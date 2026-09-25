@echo off
rem 构建 GPT365 插件（Windows）。
rem
rem 需要 MinGW-w64 的 gcc。若 gcc 不在 PATH 中，请先设置：
rem   set CC=C:\path\to\mingw64\bin\gcc.exe

setlocal

if "%CGO_ENABLED%"=="" set CGO_ENABLED=1

if not exist "build\windows\amd64" mkdir "build\windows\amd64"

echo [1/2] 运行测试...
go test ./...
if errorlevel 1 (
  echo 测试失败，已中止构建。
  exit /b 1
)

echo [2/2] 构建动态库...
go build -buildvcs=false -trimpath -ldflags="-s -w -buildid=" ^
  -buildmode=c-shared -o "build\windows\amd64\gpt365.dll" .
if errorlevel 1 (
  echo 构建失败。
  exit /b 1
)

if exist "build\windows\amd64\gpt365.h" del "build\windows\amd64\gpt365.h"

echo.
echo 已生成 build\windows\amd64\gpt365.dll
echo 请将其复制到 CPA 的 plugins\windows\amd64\ 目录后重启 CPA。

endlocal

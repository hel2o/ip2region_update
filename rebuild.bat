@echo off
REM ============================================================
REM  ip2region one-click rebuild
REM    rebuild.bat                            full rebuild (fetch + enrich + refine)
REM    rebuild.bat -skip-fetch                reuse local data, no download
REM    rebuild.bat -skip-enrich -skip-refine  re-assemble only, no network
REM    rebuild.bat -only-custom               only data\custom\*.txt changed: merge + build (no parse)
REM  Output: build\ip2region.xdb
REM  Keep this file ASCII-only: cmd parses it with the OEM codepage.
REM ============================================================
setlocal
cd /d "%~dp0"

echo [1/2] building tool ...
go build -o build\ip2region.exe .\cmd\ip2region
if errorlevel 1 (
    echo.
    echo [ERROR] go build failed - check Go toolchain and proxy settings.
    pause
    exit /b 1
)

echo [2/2] running pipeline ...
echo.
build\ip2region.exe all %*
set RC=%ERRORLEVEL%

echo.
if %RC% neq 0 (
    echo [ERROR] rebuild failed, exit code %RC%
) else (
    echo [OK] done - see build\ip2region.xdb
)
echo.

REM keep the window open on double-click, skip the pause when called with flags
if "%~1"=="" pause
exit /b %RC%

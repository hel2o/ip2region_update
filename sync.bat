@echo off
REM ============================================================
REM  ip2region sync launcher (Windows)
REM    sync.bat              sync build\ip2region.xdb to 7 targets on 3 hosts, then reload
REM    sync.bat -n           dry-run, show what would change
REM    sync.bat -keep 5      keep 5 backups per target
REM    sync.bat -skip-reload files only, do not call reload APIs
REM  Real logic lives in sync.sh - this only locates Git Bash and calls it.
REM  Keep this file ASCII-only.
REM ============================================================
setlocal
chcp 65001 >nul
cd /d "%~dp0"

set "BASH="
if exist "%ProgramFiles%\Git\bin\bash.exe" set "BASH=%ProgramFiles%\Git\bin\bash.exe"
if not defined BASH if exist "%ProgramFiles(x86)%\Git\bin\bash.exe" set "BASH=%ProgramFiles(x86)%\Git\bin\bash.exe"
if not defined BASH if exist "%LOCALAPPDATA%\Programs\Git\bin\bash.exe" set "BASH=%LOCALAPPDATA%\Programs\Git\bin\bash.exe"

if not defined BASH (
    echo [ERROR] Git Bash not found. Install Git for Windows, or run sync.sh manually.
    pause
    exit /b 1
)

"%BASH%" "%CD%\sync.sh" %*
set RC=%ERRORLEVEL%

echo.
REM keep the window open on double-click, skip the pause when called with flags
if "%~1"=="" pause
exit /b %RC%

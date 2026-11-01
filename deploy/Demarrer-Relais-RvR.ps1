# Demarrer-Relais-RvR.ps1
# Verifie, teste, construit et demarre le relais vocal RvR, puis enregistre les logs.
# Phase 1 : $Apply = $false -> verifications seules, rien n'est modifie.
# Phase 2 : $Apply = $true  -> tests unitaires, construction, demarrage, capture des logs.
# Aucun token n'est affiche : le script verifie seulement qu'ils sont renseignes.

$Apply       = $true
$ProjectDir  = "$HOME\source\aion-relay"
$WithElyos   = $true
$LogDir      = "$ProjectDir\logs"
$WaitSeconds = 30

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Test-EnvFile {
    param([string]$Path)
    if (-not (Test-Path $Path)) {
        Write-Host "[KO] $Path absent" -ForegroundColor Red
        return $false
    }
    $empty  = @()
    $filled = 0
    foreach ($line in Get-Content $Path) {
        if ($line -match '^(DISCORD_[A-Z0-9_]+)=\s*$') { $empty += $Matches[1] }
        elseif ($line -match '^DISCORD_[A-Z0-9_]+=\S+') { $filled++ }
    }
    if ($empty.Count -gt 0) {
        Write-Host "[KO] $Path : tokens vides : $($empty -join ', ')" -ForegroundColor Red
        return $false
    }
    if ($filled -lt 2) {
        Write-Host "[KO] $Path : il faut au moins le bot principal et un Heraut" -ForegroundColor Red
        return $false
    }
    Write-Host "[OK] $Path : $filled tokens renseignes" -ForegroundColor Green
    return $true
}

function Invoke-Native {
    param([string]$CommandLine, [string]$OutFile)
    # cmd /c evite que PowerShell 5.1 transforme la sortie d'erreur de docker en exception.
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    cmd /c "$CommandLine > `"$OutFile`" 2>&1"
    $code = $LASTEXITCODE
    $ErrorActionPreference = $previous
    return $code
}

# ---- Phase 1 : verifications ----
$ok = $true
Set-Location $ProjectDir

if (-not (Test-Path "$ProjectDir\docker-compose.yml")) { Write-Host "[KO] docker-compose.yml absent" -ForegroundColor Red; $ok = $false }
if (-not (Test-Path "$ProjectDir\voice-relay\internal\version\version.go")) {
    Write-Host "[KO] code source absent ou trop ancien (pas de version.go)" -ForegroundColor Red; $ok = $false
} else {
    $verLine = Select-String -Path "$ProjectDir\voice-relay\internal\version\version.go" -Pattern 'Fork = "(.+)"'
    if ($verLine) { Write-Host "[OK] version du code : $($verLine.Matches[0].Groups[1].Value)" -ForegroundColor Green }
}

$previous = $ErrorActionPreference
$ErrorActionPreference = 'Continue'
$dockerVersion = (docker version --format '{{.Server.Version}}' 2>$null)
$ErrorActionPreference = $previous
if (-not $dockerVersion) { Write-Host "[KO] Docker ne repond pas : lancer Docker Desktop" -ForegroundColor Red; $ok = $false }
else { Write-Host "[OK] Docker $dockerVersion" -ForegroundColor Green }

if (-not (Test-EnvFile "$ProjectDir\.env")) { $ok = $false }
if ($WithElyos -and -not (Test-EnvFile "$ProjectDir\.env.elyos")) { $ok = $false }

if (-not $ok) { Write-Host "Corriger les points [KO] avant de continuer." -ForegroundColor Yellow; return }
if (-not $Apply) { Write-Host "Verifications OK. Passer `$Apply a `$true pour demarrer." -ForegroundColor Cyan; return }

# ---- Phase 2 : tests, construction, demarrage ----
New-Item -ItemType Directory -Force -Path $LogDir | Out-Null
$stamp   = Get-Date -Format 'yyyyMMdd-HHmmss'
$ProfileArg = if ($WithElyos) { '--profile elyos' } else { '' }

Write-Host "Tests unitaires (quelques minutes)..." -ForegroundColor Cyan
$code = Invoke-Native "docker build --target test ./voice-relay" "$ProjectDir\test.log"
if ($code -ne 0) {
    Write-Host "[KO] tests en echec, rien n'a ete demarre. Fin de test.log :" -ForegroundColor Red
    Get-Content "$ProjectDir\test.log" -Tail 40
    return
}
Write-Host "[OK] tests reussis" -ForegroundColor Green

Write-Host "Construction et demarrage..." -ForegroundColor Cyan
$code = Invoke-Native "docker compose $ProfileArg up -d --build --remove-orphans" "$LogDir\demarrage-$stamp.log"
if ($code -ne 0) {
    Write-Host "[KO] demarrage en echec :" -ForegroundColor Red
    Get-Content "$LogDir\demarrage-$stamp.log" -Tail 40
    return
}

# Une seule capture de logs a la fois : on arrete celle du lancement precedent.
$pidFile = "$LogDir\capture.pid"
if (Test-Path $pidFile) {
    $oldPid = Get-Content $pidFile -ErrorAction SilentlyContinue
    if ($oldPid) {
        $old = Get-Process -Id $oldPid -ErrorAction SilentlyContinue
        if ($old -and $old.ProcessName -eq 'cmd') {
            Stop-Process -Id $oldPid -Force -ErrorAction SilentlyContinue
            Write-Host "Ancienne capture de logs arretee (PID $oldPid)" -ForegroundColor Gray
        }
    }
}

# cmd ne tue pas son enfant docker : arreter aussi les "docker compose logs -f" restants.
Get-CimInstance Win32_Process -Filter "Name='docker.exe'" -ErrorAction SilentlyContinue |
    Where-Object { $_.CommandLine -like '*compose*logs -f*' } |
    ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }

$logFile = "$LogDir\relais-$stamp.log"
$capture = Start-Process -FilePath 'cmd.exe' -WindowStyle Hidden -WorkingDirectory $ProjectDir -PassThru `
    -ArgumentList "/c docker compose $ProfileArg logs -f --timestamps > `"$logFile`" 2>&1"
Set-Content -Path $pidFile -Value $capture.Id -Encoding ASCII
Write-Host "Logs enregistres dans $logFile (capture invisible, PID $($capture.Id))" -ForegroundColor Green

Start-Sleep -Seconds $WaitSeconds
$text = Get-Content $logFile -Raw -ErrorAction SilentlyContinue
if (-not $text) { $text = '' }
$running = ([regex]::Matches($text, 'bot is running')).Count
$errors  = ([regex]::Matches($text, 'level=ERROR')).Count
$expected = if ($WithElyos) { 2 } else { 1 }
if ($running -ge $expected -and $errors -eq 0) {
    Write-Host "[OK] $running bot(s) principal(aux) en ligne, aucune erreur." -ForegroundColor Green
} else {
    Write-Host "[A VERIFIER] bots en ligne : $running / $expected, erreurs : $errors. Voir $logFile" -ForegroundColor Yellow
}

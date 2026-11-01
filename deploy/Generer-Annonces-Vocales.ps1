# Generer-Annonces-Vocales.ps1
# Genere les annonces vocales du relais RvR avec la synthese vocale de Windows,
# puis les convertit au format attendu par le bot (Ogg Opus, trames de 20 ms).
#
# Phase 1 ($Apply = $false) : verifie les prerequis, choisit la voix et affiche
#                             ce qui sera genere. Rien n'est modifie.
# Phase 2 ($Apply = $true)  : sauvegarde les sons actuels dans sounds\sauvegarde-<date>,
#                             genere les nouveaux fichiers .ogg dans sounds\.
#
# Ensuite : docker compose restart (dans le dossier aion-relay) pour recharger les sons.

# ---------------------------------------------------------------------------
# Parametres (a modifier ici)
# ---------------------------------------------------------------------------
$Apply      = $false
$RelayDir   = "$HOME\source\aion-relay"
$SoundsDir  = "$HOME\source\aion-relay\sounds"
$VoiceCulture = 'fr-FR'          # langue de la voix recherchee
$VoiceName  = ''                 # vide = premiere voix fr-FR trouvee (ex. 'Microsoft Hortense Desktop')
$Rate       = 1                  # vitesse de -10 (lent) a 10 (rapide)
$FfmpegImage = 'linuxserver/ffmpeg:latest'

# Caracteres accentues ecrits par leur code, pour garder ce fichier en ASCII.
$e_aigu = [char]0x00E9   # e accent aigu
$a_grave = [char]0x00E0  # a accent grave

# Nom du fichier (= nom du bouton) et texte prononce.
$Announces = [ordered]@{
    'debut'        = "D${e_aigu}but du raid. Bonne chance ${a_grave} tous !"
    'fin'          = "Fin du raid. Merci ${a_grave} tous."
    'regroupement' = "Regroupement sur le Raid Lead !"
    'attention'    = "Attention !"
    'engage'       = "Engagez ! Engagez !"
    'repli'        = "Repli ! Repli !"
}

# ---------------------------------------------------------------------------
# Verifications (les deux phases)
# ---------------------------------------------------------------------------
$ErrorActionPreference = 'Stop'

Add-Type -AssemblyName System.Speech
$synth = New-Object System.Speech.Synthesis.SpeechSynthesizer

$voices = $synth.GetInstalledVoices() | Where-Object { $_.Enabled } | ForEach-Object { $_.VoiceInfo }
Write-Host "Voix installees :"
$voices | ForEach-Object { Write-Host ("  - {0} ({1})" -f $_.Name, $_.Culture.Name) }

if ($VoiceName -ne '') {
    $voice = $voices | Where-Object { $_.Name -eq $VoiceName } | Select-Object -First 1
} else {
    $voice = $voices | Where-Object { $_.Culture.Name -eq $VoiceCulture } | Select-Object -First 1
}
if ($null -eq $voice) {
    Write-Host ""
    Write-Host "ERREUR : aucune voix $VoiceCulture installee." -ForegroundColor Red
    Write-Host "Parametres Windows > Heure et langue > Voix > Ajouter des voix > Francais (France), puis relancer."
    $synth.Dispose()
    return
}
Write-Host ""
Write-Host ("Voix retenue : {0}" -f $voice.Name) -ForegroundColor Green

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Host "ERREUR : docker introuvable. Lancer Docker Desktop puis relancer." -ForegroundColor Red
    $synth.Dispose()
    return
}
if (-not (Test-Path $SoundsDir)) {
    Write-Host "ERREUR : dossier des sons introuvable : $SoundsDir" -ForegroundColor Red
    $synth.Dispose()
    return
}

Write-Host ""
Write-Host "Annonces a generer dans $SoundsDir :"
foreach ($name in $Announces.Keys) {
    $exists = Test-Path (Join-Path $SoundsDir "$name.ogg")
    $state = if ($exists) { 'remplace' } else { 'nouveau' }
    Write-Host ("  - {0}.ogg [{1}] : {2}" -f $name, $state, $Announces[$name])
}

if (-not $Apply) {
    Write-Host ""
    Write-Host "Phase 1 terminee, rien n'a ete modifie." -ForegroundColor Yellow
    Write-Host "Pour generer : passer `$Apply = `$true en haut du script et relancer."
    $synth.Dispose()
    return
}

# ---------------------------------------------------------------------------
# Phase 2 : generation
# ---------------------------------------------------------------------------
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$backupDir = Join-Path $SoundsDir "sauvegarde-$stamp"
$workDir = Join-Path $SoundsDir "_tmp-$stamp"
New-Item -ItemType Directory -Path $backupDir | Out-Null
New-Item -ItemType Directory -Path $workDir | Out-Null

Get-ChildItem -Path $SoundsDir -Filter '*.ogg' -File | ForEach-Object {
    Copy-Item $_.FullName -Destination $backupDir
}
Write-Host "Sons actuels sauvegardes dans $backupDir"

$synth.SelectVoice($voice.Name)
$synth.Rate = $Rate
$synth.Volume = 100
$format = New-Object System.Speech.AudioFormat.SpeechAudioFormatInfo(48000, [System.Speech.AudioFormat.AudioBitsPerSample]::Sixteen, [System.Speech.AudioFormat.AudioChannel]::Mono)

$ok = 0
foreach ($name in $Announces.Keys) {
    $wav = Join-Path $workDir "$name.wav"
    $synth.SetOutputToWaveFile($wav, $format)
    $synth.Speak($Announces[$name])
    $synth.SetOutputToNull()

    # Conversion : volume normalise, 0,2 s de silence en fin, stereo 48 kHz, Opus 20 ms.
    $tmpName = "_tmp-$stamp"
    & docker run --rm -v "${SoundsDir}:/config" $FfmpegImage -hide_banner -loglevel error -y `
        -i "/config/$tmpName/$name.wav" -af "loudnorm=I=-16:TP=-1.5,apad=pad_dur=0.2" `
        -ar 48000 -ac 2 -c:a libopus -b:a 96k -frame_duration 20 -application voip "/config/$name.ogg"
    if ($LASTEXITCODE -eq 0) {
        Write-Host "  OK  $name.ogg" -ForegroundColor Green
        $ok++
    } else {
        Write-Host "  ERREUR conversion $name (code $LASTEXITCODE)" -ForegroundColor Red
    }
}
$synth.Dispose()
Remove-Item -Recurse -Force $workDir

Write-Host ""
Write-Host "$ok annonce(s) generee(s) sur $($Announces.Count)."
Write-Host "Pour les charger :"
Write-Host "  cd $RelayDir"
Write-Host "  docker compose restart"
Write-Host "Puis republier le panneau avec /panneau si la liste des sons a change."
Write-Host "Pour revenir aux carillons : copier les fichiers de $backupDir dans $SoundsDir."

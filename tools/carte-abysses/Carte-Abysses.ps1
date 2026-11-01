# Carte des Abysses : image transparente toujours au premier plan, devant le jeu.
# Simple fenetre d'image. Ne lit ni la memoire, ni le reseau, ni l'ecran du jeu.
# Prerequis : jeu en mode "Fenetre sans bordure" (le plein ecran exclusif masque toute fenetre).

# ---- Variables ----
$Dossier        = Split-Path -Parent $MyInvocation.MyCommand.Path
$DossierCartes  = Join-Path $Dossier "cartes"              # images .png / .jpg affichees, dans l'ordre alphabetique
$Reglages       = Join-Path $Dossier "carte-reglages.txt"  # position, taille, opacite (ecrit automatiquement)
$Opacite        = 0.7                                      # 0.2 a 1.0, au premier lancement
$UrlCarte       = ""                                       # optionnel : lien direct vers l'image du raid, retelechargee
$RafraichirSec  = 60                                       # intervalle de telechargement si $UrlCarte est rempli
$Journal        = Join-Path $Dossier "carte.log"

# ---- Execution ----
try {
    $source = Get-Content -Path (Join-Path $Dossier "Overlay.cs") -Raw -Encoding UTF8
    Add-Type -TypeDefinition $source -ReferencedAssemblies System.Windows.Forms, System.Drawing, System.Core -ErrorAction Stop
    [CarteAbysses.Overlay]::Run($DossierCartes, $Reglages, $Opacite, $UrlCarte, $RafraichirSec)
}
catch {
    ("{0} {1}" -f (Get-Date -Format "dd/MM/yyyy HH:mm:ss"), $_.Exception.Message) | Out-File -FilePath $Journal -Append -Encoding UTF8
    Add-Type -AssemblyName System.Windows.Forms
    [System.Windows.Forms.MessageBox]::Show("La carte n'a pas pu demarrer : " + $_.Exception.Message, "Carte des Abysses") | Out-Null
}

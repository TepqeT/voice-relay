using System;
using System.Collections.Generic;
using System.Drawing;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Net;
using System.Runtime.InteropServices;
using System.Windows.Forms;

namespace CarteAbysses
{
    // Transparent, always-on-top window showing a map image above the game.
    // It never reads or touches the game: it is a picture pinned on the screen.
    public static class Program
    {
        [STAThread]
        public static void Main(string[] args)
        {
            string dir = AppDomain.CurrentDomain.BaseDirectory;
            Overlay.Run(Path.Combine(dir, "cartes"), Path.Combine(dir, "carte-reglages.txt"), 0.7, "", 60);
        }
    }

    public class Overlay : Form
    {
        // ---- Win32 ----
        [DllImport("user32.dll")] static extern bool RegisterHotKey(IntPtr hWnd, int id, uint mods, uint vk);
        [DllImport("user32.dll")] static extern bool UnregisterHotKey(IntPtr hWnd, int id);
        [DllImport("user32.dll")] static extern int GetWindowLong(IntPtr hWnd, int index);
        [DllImport("user32.dll")] static extern int SetWindowLong(IntPtr hWnd, int index, int value);
        [DllImport("user32.dll")] static extern bool SetWindowPos(IntPtr hWnd, IntPtr after, int x, int y, int cx, int cy, uint flags);
        [DllImport("user32.dll")] static extern bool ReleaseCapture();
        [DllImport("user32.dll")] static extern IntPtr SendMessage(IntPtr hWnd, int msg, IntPtr w, IntPtr l);

        const int GWL_EXSTYLE = -20;
        const int WS_EX_LAYERED = 0x80000, WS_EX_TRANSPARENT = 0x20, WS_EX_TOOLWINDOW = 0x80, WS_EX_NOACTIVATE = 0x8000000;
        const int WM_HOTKEY = 0x0312, WM_NCHITTEST = 0x84, WM_NCLBUTTONDOWN = 0xA1;
        const int HTCAPTION = 2, HTBOTTOMRIGHT = 17;
        const uint MOD_ALT = 1, MOD_CTRL = 2, MOD_NOREPEAT = 0x4000;
        const uint SWP_NOMOVE = 2, SWP_NOSIZE = 1, SWP_NOACTIVATE = 0x10;
        static readonly IntPtr HWND_TOPMOST = new IntPtr(-1);

        const int HK_TOGGLE = 1, HK_LOCK = 2, HK_MORE = 3, HK_LESS = 4, HK_NEXT = 5, HK_QUIT = 6;

        // ---- state ----
        readonly string mapsDir, settingsFile, imageUrl;
        readonly int refreshSeconds;
        List<string> maps = new List<string>();
        int mapIndex;
        bool locked = true;
        readonly PictureBox pic = new PictureBox();
        readonly NotifyIcon tray = new NotifyIcon();
        readonly Timer topmostTimer = new Timer();
        readonly Timer urlTimer = new Timer();
        FileSystemWatcher watcher;
        readonly Label hint = new Label();

        public Overlay(string mapsDir, string settingsFile, double opacity, string imageUrl, int refreshSeconds)
        {
            this.mapsDir = mapsDir;
            this.settingsFile = settingsFile;
            this.imageUrl = imageUrl ?? "";
            this.refreshSeconds = Math.Max(15, refreshSeconds);

            Text = "Carte des Abysses";
            FormBorderStyle = FormBorderStyle.None;
            ShowInTaskbar = false;
            TopMost = true;
            StartPosition = FormStartPosition.Manual;
            BackColor = Color.Magenta;
            TransparencyKey = Color.Magenta;
            Opacity = opacity;
            Bounds = new Rectangle(40, 120, 480, 480);
            MinimumSize = new Size(120, 120);

            pic.Dock = DockStyle.Fill;
            pic.SizeMode = PictureBoxSizeMode.Zoom;
            pic.BackColor = Color.Magenta;
            pic.MouseDown += DragWindow;
            Controls.Add(pic);

            hint.Dock = DockStyle.Bottom;
            hint.Height = 22;
            hint.BackColor = Color.FromArgb(20, 20, 30);
            hint.ForeColor = Color.Gold;
            hint.TextAlign = ContentAlignment.MiddleCenter;
            hint.Text = "Deverrouille : glisser pour deplacer, coin bas-droit ou molette pour la taille, Ctrl+Alt+L pour verrouiller";
            hint.MouseDown += DragWindow;
            Controls.Add(hint);

            MouseWheel += OnWheel;
            pic.MouseWheel += OnWheel;

            LoadSettings();
            BuildTray();
            LoadMapList();
            ShowMap();

            topmostTimer.Interval = 2000;
            topmostTimer.Tick += delegate { if (Visible) SetWindowPos(Handle, HWND_TOPMOST, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_NOACTIVATE); };
            topmostTimer.Start();

            if (this.imageUrl.Length > 0)
            {
                urlTimer.Interval = this.refreshSeconds * 1000;
                urlTimer.Tick += delegate { DownloadMap(); };
                urlTimer.Start();
                DownloadMap();
            }

            try
            {
                Directory.CreateDirectory(mapsDir);
                watcher = new FileSystemWatcher(mapsDir);
                watcher.IncludeSubdirectories = false;
                watcher.NotifyFilter = NotifyFilters.FileName | NotifyFilters.LastWrite;
                watcher.SynchronizingObject = this;
                watcher.Changed += delegate { ReloadSoon(); };
                watcher.Created += delegate { ReloadSoon(); };
                watcher.Renamed += delegate { ReloadSoon(); };
                watcher.Deleted += delegate { ReloadSoon(); };
                watcher.EnableRaisingEvents = true;
            }
            catch (Exception) { }
        }

        // ---- window styles ----
        protected override CreateParams CreateParams
        {
            get
            {
                CreateParams cp = base.CreateParams;
                cp.ExStyle |= WS_EX_LAYERED | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE;
                if (locked) cp.ExStyle |= WS_EX_TRANSPARENT;
                return cp;
            }
        }

        protected override bool ShowWithoutActivation { get { return true; } }

        protected override void OnHandleCreated(EventArgs e)
        {
            base.OnHandleCreated(e);
            RegisterHotKey(Handle, HK_TOGGLE, MOD_CTRL | MOD_ALT | MOD_NOREPEAT, (uint)Keys.M);
            RegisterHotKey(Handle, HK_LOCK, MOD_CTRL | MOD_ALT | MOD_NOREPEAT, (uint)Keys.L);
            RegisterHotKey(Handle, HK_MORE, MOD_CTRL | MOD_ALT, (uint)Keys.Up);
            RegisterHotKey(Handle, HK_LESS, MOD_CTRL | MOD_ALT, (uint)Keys.Down);
            RegisterHotKey(Handle, HK_NEXT, MOD_CTRL | MOD_ALT | MOD_NOREPEAT, (uint)Keys.N);
            RegisterHotKey(Handle, HK_QUIT, MOD_CTRL | MOD_ALT | MOD_NOREPEAT, (uint)Keys.Q);
            ApplyLock();
        }

        protected override void OnFormClosed(FormClosedEventArgs e)
        {
            for (int i = HK_TOGGLE; i <= HK_QUIT; i++) UnregisterHotKey(Handle, i);
            SaveSettings();
            tray.Visible = false;
            tray.Dispose();
            base.OnFormClosed(e);
        }

        protected override void WndProc(ref Message m)
        {
            if (m.Msg == WM_HOTKEY)
            {
                switch (m.WParam.ToInt32())
                {
                    case HK_TOGGLE: ToggleVisible(); break;
                    case HK_LOCK: locked = !locked; ApplyLock(); SaveSettings(); break;
                    case HK_MORE: SetOpacity(Opacity + 0.1); break;
                    case HK_LESS: SetOpacity(Opacity - 0.1); break;
                    case HK_NEXT: NextMap(); break;
                    case HK_QUIT: Close(); break;
                }
                return;
            }
            base.WndProc(ref m);
            if (m.Msg == WM_NCHITTEST && !locked)
            {
                int lp = unchecked((int)m.LParam.ToInt64());
                Point p = PointToClient(new Point((short)(lp & 0xFFFF), (short)((lp >> 16) & 0xFFFF)));
                if (p.X >= ClientSize.Width - 18 && p.Y >= ClientSize.Height - 18) m.Result = (IntPtr)HTBOTTOMRIGHT;
            }
        }

        void ApplyLock()
        {
            int ex = GetWindowLong(Handle, GWL_EXSTYLE);
            if (locked) ex |= WS_EX_TRANSPARENT; else ex &= ~WS_EX_TRANSPARENT;
            SetWindowLong(Handle, GWL_EXSTYLE, ex);
            hint.Visible = !locked;
            Padding = locked ? new Padding(0) : new Padding(3);
            BackColor = locked ? Color.Magenta : Color.Gold;
            lockItem.Text = locked ? "Deverrouiller (deplacer, redimensionner)" : "Verrouiller (la souris traverse la carte)";
        }

        void DragWindow(object sender, MouseEventArgs e)
        {
            if (locked || e.Button != MouseButtons.Left) return;
            ReleaseCapture();
            SendMessage(Handle, WM_NCLBUTTONDOWN, (IntPtr)HTCAPTION, IntPtr.Zero);
            SaveSettings();
        }

        void OnWheel(object sender, MouseEventArgs e)
        {
            if (locked) return;
            double f = e.Delta > 0 ? 1.1 : 1 / 1.1;
            Size = new Size(Math.Max(120, (int)(Width * f)), Math.Max(120, (int)(Height * f)));
            SaveSettings();
        }

        void SetOpacity(double v)
        {
            Opacity = Math.Min(1.0, Math.Max(0.2, Math.Round(v, 1)));
            ApplyLock();
            SaveSettings();
        }

        void ToggleVisible()
        {
            if (Visible) Hide(); else { Show(); SetWindowPos(Handle, HWND_TOPMOST, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_NOACTIVATE); }
        }

        // ---- maps ----
        void LoadMapList()
        {
            maps = new List<string>();
            try
            {
                foreach (string ext in new[] { "*.png", "*.jpg", "*.jpeg" })
                    maps.AddRange(Directory.GetFiles(mapsDir, ext));
            }
            catch (Exception) { }
            maps = maps.Distinct().OrderBy(p => Path.GetFileName(p), StringComparer.OrdinalIgnoreCase).ToList();
            if (mapIndex >= maps.Count) mapIndex = 0;
        }

        void ShowMap()
        {
            Image old = pic.Image;
            pic.Image = null;
            if (old != null) old.Dispose();
            if (maps.Count == 0)
            {
                tray.Text = "Carte des Abysses : aucune image";
                Bitmap b = new Bitmap(480, 160);
                using (Graphics g = Graphics.FromImage(b))
                using (Font f = new Font("Segoe UI", 13f, FontStyle.Bold))
                {
                    g.Clear(Color.FromArgb(20, 20, 30));
                    g.DrawString("Aucune carte.\nMets une image .png ou .jpg dans le dossier\n" + mapsDir, f, Brushes.Gold, new RectangleF(12, 12, 456, 136));
                }
                pic.Image = b;
                return;
            }
            string path = maps[mapIndex];
            try
            {
                // Copy the bytes so the file is never locked and can be replaced at any time.
                byte[] bytes = File.ReadAllBytes(path);
                using (MemoryStream ms = new MemoryStream(bytes))
                using (Image img = Image.FromStream(ms))
                    pic.Image = new Bitmap(img);
                tray.Text = Truncate("Carte : " + Path.GetFileNameWithoutExtension(path), 63);
            }
            catch (Exception) { /* file being written: the watcher reloads it */ }
        }

        void NextMap()
        {
            LoadMapList();
            if (maps.Count == 0) return;
            mapIndex = (mapIndex + 1) % maps.Count;
            ShowMap();
            SaveSettings();
        }

        Timer reloadTimer;
        void ReloadSoon()
        {
            if (reloadTimer == null)
            {
                reloadTimer = new Timer();
                reloadTimer.Interval = 700;
                reloadTimer.Tick += delegate { reloadTimer.Stop(); string cur = maps.Count > 0 ? maps[mapIndex] : null; LoadMapList(); if (cur != null && maps.Contains(cur)) mapIndex = maps.IndexOf(cur); ShowMap(); };
            }
            reloadTimer.Stop();
            reloadTimer.Start();
        }

        void DownloadMap()
        {
            try
            {
                ServicePointManager.SecurityProtocol = SecurityProtocolType.Tls12;
                using (WebClient wc = new WebClient())
                {
                    byte[] data = wc.DownloadData(imageUrl);
                    string target = Path.Combine(mapsDir, "00-carte-du-raid.png");
                    string tmp = target + ".tmp";
                    File.WriteAllBytes(tmp, data);
                    if (File.Exists(target)) File.Delete(target);
                    File.Move(tmp, target);
                }
            }
            catch (Exception) { /* offline or bad link: keep the current map */ }
        }

        // ---- tray ----
        readonly ToolStripMenuItem lockItem = new ToolStripMenuItem();
        void BuildTray()
        {
            ContextMenuStrip menu = new ContextMenuStrip();
            menu.Items.Add("Afficher / masquer (Ctrl+Alt+M)", null, delegate { ToggleVisible(); });
            lockItem.Click += delegate { locked = !locked; ApplyLock(); SaveSettings(); };
            menu.Items.Add(lockItem);
            menu.Items.Add("Carte suivante (Ctrl+Alt+N)", null, delegate { NextMap(); });
            menu.Items.Add("Plus opaque (Ctrl+Alt+Haut)", null, delegate { SetOpacity(Opacity + 0.1); });
            menu.Items.Add("Plus transparent (Ctrl+Alt+Bas)", null, delegate { SetOpacity(Opacity - 0.1); });
            menu.Items.Add("Ouvrir le dossier des cartes", null, delegate { try { System.Diagnostics.Process.Start("explorer.exe", "\"" + mapsDir + "\""); } catch (Exception) { } });
            menu.Items.Add(new ToolStripSeparator());
            menu.Items.Add("Quitter (Ctrl+Alt+Q)", null, delegate { Close(); });
            tray.ContextMenuStrip = menu;
            tray.Icon = SystemIcons.Information;
            tray.Text = "Carte des Abysses";
            tray.Visible = true;
            tray.DoubleClick += delegate { ToggleVisible(); };
        }

        // ---- settings (key=value, next to the script) ----
        void LoadSettings()
        {
            try
            {
                if (!File.Exists(settingsFile)) return;
                Dictionary<string, string> kv = new Dictionary<string, string>();
                foreach (string line in File.ReadAllLines(settingsFile))
                {
                    int i = line.IndexOf('=');
                    if (i > 0) kv[line.Substring(0, i).Trim()] = line.Substring(i + 1).Trim();
                }
                int x, y, w, h, idx; double op;
                if (Int(kv, "x", out x) && Int(kv, "y", out y) && Int(kv, "w", out w) && Int(kv, "h", out h))
                {
                    Rectangle r = new Rectangle(x, y, Math.Max(120, w), Math.Max(120, h));
                    // Back on screen if the saved place is on a monitor that is gone.
                    if (Screen.AllScreens.Any(s => s.WorkingArea.IntersectsWith(r))) Bounds = r;
                }
                if (kv.ContainsKey("opacity") && double.TryParse(kv["opacity"], NumberStyles.Float, CultureInfo.InvariantCulture, out op)) Opacity = Math.Min(1.0, Math.Max(0.2, op));
                if (Int(kv, "map", out idx)) mapIndex = Math.Max(0, idx);
                if (kv.ContainsKey("locked")) locked = kv["locked"] != "0";
            }
            catch (Exception) { }
        }

        static bool Int(Dictionary<string, string> kv, string k, out int v)
        {
            v = 0;
            return kv.ContainsKey(k) && int.TryParse(kv[k], NumberStyles.Integer, CultureInfo.InvariantCulture, out v);
        }

        void SaveSettings()
        {
            try
            {
                Rectangle b = Bounds;
                File.WriteAllLines(settingsFile, new[] {
                    "x=" + b.X, "y=" + b.Y, "w=" + b.Width, "h=" + b.Height,
                    "opacity=" + Opacity.ToString("0.0", CultureInfo.InvariantCulture),
                    "map=" + mapIndex, "locked=" + (locked ? "1" : "0") });
            }
            catch (Exception) { }
        }

        static string Truncate(string s, int n) { return s.Length <= n ? s : s.Substring(0, n); }

        public static void Run(string mapsDir, string settingsFile, double opacity, string imageUrl, int refreshSeconds)
        {
            Application.EnableVisualStyles();
            Application.Run(new Overlay(mapsDir, settingsFile, opacity, imageUrl, refreshSeconds));
        }
    }
}

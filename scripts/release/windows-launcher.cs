using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Reflection;
using System.Windows.Forms;
using Microsoft.Win32;

[assembly: AssemblyTitle("ARK Studio Tray Launcher")]
[assembly: AssemblyVersion("2.0.0.0")]

static class Launcher
{
    static string Quote(string value) { return "\"" + value.Replace("\"", "\\\"") + "\""; }
    static readonly string Settings = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.UserProfile), ".ark", "launcher-python.txt");

    static bool Ready(string python)
    {
        try
        {
            var start = new ProcessStartInfo(python,
                "-c " + Quote("import sys,importlib.util; sys.exit(0 if sys.version_info >= (3,10) and all(importlib.util.find_spec(m) for m in ('webview','pystray','PIL','tkinter')) else 1)"));
            start.UseShellExecute = false;
            start.CreateNoWindow = true;
            using (var process = Process.Start(start))
            {
                if (!process.WaitForExit(10000)) { process.Kill(); return false; }
                return process.ExitCode == 0;
            }
        }
        catch { return false; }
    }

    static IEnumerable<string> Candidates(string root)
    {
        var configured = Environment.GetEnvironmentVariable("ARK_PYTHON");
        if (!String.IsNullOrWhiteSpace(configured)) yield return configured;
        if (File.Exists(Settings)) yield return File.ReadAllText(Settings).Trim();
        yield return Path.Combine(root, ".venv", "Scripts", "python.exe");
        yield return Path.Combine(root, "venv", "Scripts", "python.exe");
        var active = Environment.GetEnvironmentVariable("VIRTUAL_ENV");
        if (!String.IsNullOrWhiteSpace(active)) yield return Path.Combine(active, "Scripts", "python.exe");
        yield return "python.exe";
        foreach (var hive in new[] { Registry.CurrentUser, Registry.LocalMachine })
        using (var core = hive.OpenSubKey(@"Software\Python\PythonCore"))
        {
            if (core == null) continue;
            foreach (string version in core.GetSubKeyNames())
            using (var install = core.OpenSubKey(version + @"\InstallPath"))
            {
                if (install == null) continue;
                var path = install.GetValue("ExecutablePath") as string;
                if (String.IsNullOrEmpty(path)) path = Path.Combine((install.GetValue("") as string) ?? "", "python.exe");
                yield return path;
            }
        }
    }

    [STAThread]
    static int Main()
    {
        Application.EnableVisualStyles();
        try
        {
            var root = AppDomain.CurrentDomain.BaseDirectory;
            if (!File.Exists(Path.Combine(root, "arkstudio", "launcher.py")))
                throw new Exception("Keep ARK-Tray.exe in the ARK Studio repository. Create a Windows shortcut to it instead of moving the executable.");
            string python = null;
            var tried = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            foreach (string candidate in Candidates(root))
                if (tried.Add(candidate) && Ready(candidate)) { python = candidate; break; }
            while (python == null)
            {
                var answer = MessageBox.Show("ARK requires Python 3.10+ and its desktop dependencies.\n\nInstall them with:\npython -m pip install -r requirements-desktop.txt\n\nSelect the python.exe belonging to that environment?", "ARK Studio setup", MessageBoxButtons.OKCancel, MessageBoxIcon.Information);
                if (answer != DialogResult.OK) return 0;
                using (var dialog = new OpenFileDialog())
                {
                    dialog.Title = "Select the ARK Python interpreter";
                    dialog.Filter = "Python interpreter (python.exe)|python.exe";
                    if (dialog.ShowDialog() != DialogResult.OK) return 0;
                    if (Ready(dialog.FileName)) python = dialog.FileName;
                }
            }
            Directory.CreateDirectory(Path.GetDirectoryName(Settings));
            // Persist the resolved path, including when the first probe used PATH.
            if (Path.IsPathRooted(python)) File.WriteAllText(Settings, python);
            string windowed = Path.Combine(Path.GetDirectoryName(python) ?? "", "pythonw.exe");
            var launch = new ProcessStartInfo(File.Exists(windowed) ? windowed : python, "-m arkstudio.launcher");
            launch.WorkingDirectory = root;
            launch.UseShellExecute = false;
            launch.CreateNoWindow = true;
            Process.Start(launch);
            return 0;
        }
        catch (Exception error)
        {
            MessageBox.Show(error.Message, "ARK could not start", MessageBoxButtons.OK, MessageBoxIcon.Error);
            return 1;
        }
    }
}

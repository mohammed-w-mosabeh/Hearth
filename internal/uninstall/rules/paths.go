// Package rules holds the rules of "Uninstall completely" (SECURITY S16k,
// S16l) that hearthd and hearth-broker both enforce: which paths may be
// leftovers, and the shapes of operation IDs and manifests. It does no
// I/O and imports nothing of Hearth's, so the broker stays small and both
// sides check exactly the same thing.
package rules

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// SystemRoots are the folders whose direct children may be system
// leftovers. /usr/local/bin holds files only.
var SystemRoots = []string{"/etc", "/var/lib", "/var/cache", "/var/log", "/opt", "/usr/local/bin"}

// UserRoots are the folders, relative to the home, whose direct children
// may be user leftovers. The home itself is a root too, for dot-folders.
var UserRoots = []string{".config", ".local/share", ".local/state", ".cache", ".var/app", "snap"}

// maxPath bounds a leftover path.
const maxPath = 4096

// systemNameRe is a system leftover's name: package and program names.
// No space, no leading dot or dash.
var systemNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+@-]{0,254}$`)

// userNameRe is a user leftover's name below a root. Apps name their
// folders freely ("Code - OSS"), so spaces and parentheses are allowed
// inside, never at an end, and never a leading dash.
var userNameRe = regexp.MustCompile(`^[A-Za-z0-9_](?:[A-Za-z0-9_.+@ ()-]{0,253}[A-Za-z0-9_.+@()-])?$`)

// sharedSystem are children of the system roots that are never selected,
// whoever owns them. dpkg owns most of them already; this list does not
// depend on that.
var sharedSystem = map[string]bool{
	"hearth": true,
	// /etc
	"alternatives": true, "apt": true, "default": true, "systemd": true, "ssl": true, "ssh": true, "pam.d": true,
	"security": true, "sudoers.d": true, "polkit-1": true, "dbus-1": true, "udev": true, "X11": true, "xdg": true,
	"init.d": true, "network": true, "NetworkManager": true, "modprobe.d": true, "sysctl.d": true, "profile.d": true,
	"ld.so.conf.d": true, "logrotate.d": true, "cron.d": true, "cron.daily": true, "cron.hourly": true,
	"cron.weekly": true, "cron.monthly": true, "dpkg": true, "fonts": true, "skel": true, "kernel": true, "grub.d": true,
	"initramfs-tools": true, "environment.d": true, "tmpfiles.d": true, "binfmt.d": true, "modules-load.d": true,
	// /var/lib
	"polkit": true, "flatpak": true, "snapd": true, "PackageKit": true, "ucf": true, "misc": true, "private": true,
	"sudo": true, "pam": true, "dbus": true, "AccountsService": true, "gdm3": true, "lightdm": true, "sddm": true,
	// /var/cache
	"debconf": true, "man": true, "fontconfig": true, "ldconfig": true, "private-tmp": true,
	// /var/log
	"journal": true, "installer": true, "apt-history": true,
	// /opt
	"local": true,
}

// sharedUser are children of the user roots, and dot-folders of the home,
// that are never selected: folders many apps share, and Hearth's own.
var sharedUser = map[string]map[string]bool{
	".config": set("hearth", "autostart", "systemd", "dconf", "gtk-2.0", "gtk-3.0", "gtk-4.0", "pulse", "menus",
		"environment.d", "fontconfig", "mimeapps.list", "user-dirs.dirs", "user-dirs.locale", "monitors.xml",
		"gnome-session", "xsettingsd", "Trolltech.conf", "kdeglobals", "ibus", "enchant", "procps", "dconf.ini"),
	".local/share": set("hearth", "applications", "icons", "fonts", "Trash", "keyrings", "mime", "flatpak",
		"gvfs-metadata", "recently-used.xbel", "themes", "sounds", "desktop-directories", "gnome-shell",
		"systemd", "xorg", "sessions", "backgrounds", "nautilus", "tracker", "tracker3", "containers", "man"),
	".local/state": set("hearth", "wireplumber", "systemd", "pipewire"),
	".cache": set("hearth", "fontconfig", "thumbnails", "mesa_shader_cache", "mesa_shader_cache_db", "ibus",
		"tracker", "tracker3", "gstreamer-1.0", "pip", "go-build", "flatpak", "dconf", "nvidia", "radv_builtin_shaders"),
	".var/app": set(),
	"snap":     set(),
	// Dot-folders of the home itself; names are compared without the dot.
	"": set("config", "local", "cache", "var", "ssh", "gnupg", "pki", "password-store", "bashrc", "bash_profile",
		"bash_logout", "bash_history", "profile", "zshrc", "zshenv", "zprofile", "zsh_history", "Xauthority",
		"ICEauthority", "dbus", "themes", "icons", "fonts", "face", "xinitrc", "xsession", "xsessionrc",
		"Xresources", "inputrc", "selected_editor", "sudo_as_admin_successful", "lesshst", "wget-hsts", "mozilla",
		"hearth", "kube", "aws", "azure", "netrc", "git-credentials", "gitconfig"),
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// cleanAbs checks the shape every leftover path shares.
func cleanAbs(p string) error {
	if p == "" || len(p) > maxPath {
		return errors.New("path is empty or too long")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0xfffd {
			return errors.New("path has a control character")
		}
	}
	if !path.IsAbs(p) || path.Clean(p) != p {
		return errors.New("path must be absolute and clean")
	}
	for _, el := range strings.Split(p[1:], "/") {
		if el == ".." || el == "." || strings.HasPrefix(el, "-") {
			return fmt.Errorf("path has a bad element %q", el)
		}
	}
	return nil
}

// CheckSystem checks that p may be a system leftover: a direct child of
// one of SystemRoots, named as a package or program, not shared. It
// returns the root and the name.
func CheckSystem(p string) (root, name string, err error) {
	if err := cleanAbs(p); err != nil {
		return "", "", err
	}
	dir, name := path.Split(p)
	root = strings.TrimSuffix(dir, "/")
	ok := false
	for _, r := range SystemRoots {
		if root == r {
			ok = true
			break
		}
	}
	if !ok {
		return "", "", fmt.Errorf("%s is not directly in %s", p, strings.Join(SystemRoots, ", "))
	}
	if !systemNameRe.MatchString(name) {
		return "", "", fmt.Errorf("%q is not a package or program name", name)
	}
	if sharedSystem[name] {
		return "", "", fmt.Errorf("%s is shared and never removed", p)
	}
	return root, name, nil
}

// CheckUser checks that p may be a user leftover of home: a direct child
// of one of UserRoots, or a dot-folder of the home itself, not shared. It
// returns the root relative to home ("" for the home itself) and the
// name.
func CheckUser(home, p string) (root, name string, err error) {
	if err := cleanAbs(home); err != nil || home == "/" {
		return "", "", errors.New("bad home folder")
	}
	if err := cleanAbs(p); err != nil {
		return "", "", err
	}
	rel, ok := strings.CutPrefix(p, home+"/")
	if !ok || rel == "" {
		return "", "", fmt.Errorf("%s is not in the home folder", p)
	}
	dir, name := path.Split(rel)
	root = strings.TrimSuffix(dir, "/")
	switch {
	case root == "":
		bare, dot := strings.CutPrefix(name, ".")
		if !dot || !userNameRe.MatchString(bare) || sharedUser[""][bare] {
			return "", "", fmt.Errorf("%s is not a dot-folder that may be removed", p)
		}
		return "", name, nil
	case sharedUser[root] != nil:
		if !userNameRe.MatchString(name) {
			return "", "", fmt.Errorf("%q is not a name that may be removed", name)
		}
		if sharedUser[root][name] {
			return "", "", fmt.Errorf("%s is shared and never removed", p)
		}
		return root, name, nil
	}
	return "", "", fmt.Errorf("%s is not directly in one of %s", p, strings.Join(UserRoots, ", "))
}

// SystemName reports whether s is a system leftover's name: a package or
// program name, no space, no leading dot or dash.
func SystemName(s string) bool { return systemNameRe.MatchString(s) }

// SharedUser reports whether name, below the user root root ("" for the
// home's dot-folders, compared without the dot), is shared and never
// removed.
func SharedUser(root, name string) bool {
	m, ok := sharedUser[root]
	return !ok || m[name]
}

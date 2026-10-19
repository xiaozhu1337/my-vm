package vm

// GuestSystem describes a guest profile without changing an existing installation.
type GuestSystem struct {
	ID, Name string
	Linux    bool
}

var GuestSystems = []GuestSystem{
	{ID: "windows10", Name: "Windows 10"},
	{ID: "windows11", Name: "Windows 11"},
	{ID: "ubuntu26", Name: "Ubuntu 26", Linux: true},
	{ID: "windows", Name: "Windows（其他版本）"},
	{ID: "linux", Name: "Linux（其他发行版）", Linux: true},
}

func (m Machine) guestSystem() (GuestSystem, bool) {
	id := m.GuestOS
	if id == "" {
		id = "windows"
	}
	for _, system := range GuestSystems {
		if system.ID == id {
			return system, true
		}
	}
	return GuestSystem{}, false
}

func (m Machine) IsLinux() bool {
	system, _ := m.guestSystem()
	return system.Linux
}

func (m Machine) GuestName() string {
	system, _ := m.guestSystem()
	return system.Name
}

func (m Machine) DesktopMode() string {
	if m.LinuxDesktop != "" {
		return m.LinuxDesktop
	}
	if m.GuestOS == "ubuntu26" {
		return "wayland"
	}
	return "x11"
}

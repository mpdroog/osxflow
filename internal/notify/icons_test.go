package notify

import (
	"testing"

	"github.com/mpdroog/osxflow/internal/desktop"
)

func TestIconIndex(t *testing.T) {
	apps := []desktop.App{
		{ID: "thunderbird.desktop", Name: "Thunderbird Mail", Icon: "thunderbird", Binary: "/usr/bin/thunderbird"},
		{ID: "com.discordapp.Discord.desktop", Name: "Discord", Icon: "discord", StartupWMClass: "discord"},
		{ID: "noicon.desktop", Name: "No Icon", Icon: "noicon"},
		{ID: "org.gnome.Calendar.desktop", Name: "Calendar", Icon: "/opt/cal/icon.png"},
	}
	ix := NewIconIndex(apps, func(key string) bool { return key != "noicon" })

	for _, tc := range []struct {
		name string
		n    Notification
		want string
	}{
		{"theme icon name", Notification{Icon: Icon{Name: "thunderbird"}}, "thunderbird"},
		{"display name", Notification{AppName: "Thunderbird Mail"}, "thunderbird"},
		{"reverse-DNS desktop entry", Notification{DesktopEntry: "org.mozilla.Thunderbird"}, "thunderbird"},
		{"WM class", Notification{AppName: "discord"}, "com.discordapp.Discord"},
		{"desktop entry, any case", Notification{DesktopEntry: "com.discordapp.discord"}, "com.discordapp.Discord"},
		{"desktop entry with suffix", Notification{DesktopEntry: "org.gnome.Calendar.desktop"}, "org.gnome.Calendar"},
		{"app without an embedded icon", Notification{AppName: "No Icon"}, ""},
		{"unknown sender", Notification{AppName: "notify-send"}, ""},
		{"nothing at all", Notification{}, ""},
		{"icon name outranks app name", Notification{Icon: Icon{Name: "discord"}, AppName: "Thunderbird Mail"}, "com.discordapp.Discord"},
		{"an unknown icon name falls through", Notification{Icon: Icon{Name: "mail-unread"}, AppName: "Thunderbird Mail"}, "thunderbird"},
	} {
		if got := ix.Lookup(&tc.n); got != tc.want {
			t.Errorf("%s: Lookup = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestIconIndexIgnoresPathIcons(t *testing.T) {
	apps := []desktop.App{{ID: "cal.desktop", Name: "Calendar", Icon: "/opt/cal/icon.png"}}
	ix := NewIconIndex(apps, func(string) bool { return true })
	if got := ix.Lookup(&Notification{Icon: Icon{Name: "/opt/cal/icon.png"}}); got != "" {
		t.Errorf("Lookup by an icon path = %q, want nothing: paths are not names", got)
	}
}

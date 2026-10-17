package convert

import (
	"fmt"
	"sort"
	"strings"
)

// Device presets (--for): the settings a player plays best, as flag
// values, given unless the command line gives them itself.

// Device is a player's preset.
type Device struct {
	// Flags are the settings, by flag name.
	Flags map[string]string
	// Ext is the output file's kind it plays ("" for any).
	Ext string
	// About says what the preset is for and does.
	About string
}

// Devices are the presets by name.
var Devices = map[string]Device{
	"coreelec": {Ext: ".mkv", About: "CoreELEC on an Amlogic box (Ugoos AM6B+ and alike): HEVC 10-bit, Dolby Vision profile 7 with its full enhancement layer kept, 3D side by side (--layout mvc for MVC), the disc's subtitles as they are",
		Flags: map[string]string{"codec": "h265", "bit-depth": "10", "dv-fel": "keep", "layout": "full"}},
	"kodi": {Ext: ".mkv", About: "Kodi elsewhere: HEVC 10-bit, Dolby Vision profile 8.1 (the enhancement layer composed in), 3D side by side",
		Flags: map[string]string{"codec": "h265", "bit-depth": "10", "dv-fel": "compose", "layout": "full"}},
	"plex": {Ext: ".mkv", About: "Plex and Jellyfin, played without transcoding: HEVC 10-bit, Dolby Vision 8.1, picture subtitles read as text (clients transcode PGS), 3D half side by side",
		Flags: map[string]string{"codec": "h265", "bit-depth": "10", "dv-fel": "compose", "subs-ocr": "true", "layout": "half"}},
	"appletv": {Ext: ".mp4", About: "Apple TV and other Apple players: an MP4 of HEVC 10-bit, the sound as AC-3 where the file cannot carry the source's, subtitles as text",
		Flags: map[string]string{"codec": "h265", "bit-depth": "10", "subs-ocr": "true", "audio-transcode": "ac3"}},
	"visionpro": {Ext: ".mov", About: "Apple Vision Pro and iPhone spatial video: MV-HEVC (both eyes in one stream), subtitles as text",
		Flags: map[string]string{"layout": "mvhevc", "subs-ocr": "true"}},
	"quest": {Ext: ".mp4", About: "Meta Quest players (SKYBOX, Moon): MV-HEVC in MP4; their subtitles are burned in with --burn-subs",
		Flags: map[string]string{"layout": "mvhevc"}},
}

// DeviceNames lists the presets.
func DeviceNames() string {
	var n []string
	for k := range Devices {
		n = append(n, k)
	}
	sort.Strings(n)
	return strings.Join(n, ", ")
}

// DeviceFor finds a preset.
func DeviceFor(name string) (Device, error) {
	d, ok := Devices[strings.ToLower(name)]
	if !ok {
		return Device{}, fmt.Errorf("--for %q: want one of %s", name, DeviceNames())
	}
	return d, nil
}

package tui

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	kittyImagePrefix  = "\x1b_G"
	iterm2ImagePrefix = "\x1b]1337;File="
)

// IsImageLine reports whether a rendered row contains a Kitty or iTerm2 image
// command. Image rows must never be rebuilt as text.
func IsImageLine(line string) bool {
	return strings.Contains(line, kittyImagePrefix) || strings.Contains(line, iterm2ImagePrefix)
}

// DeleteKittyImage deletes an image and its uploaded data by ID.
func DeleteKittyImage(imageID uint32) string {
	return "\x1b_Ga=d,d=I,i=" + strconv.FormatUint(uint64(imageID), 10) + ",q=2\x1b\\"
}

// DeleteAllKittyImages deletes every visible image and its uploaded data.
func DeleteAllKittyImages() string { return "\x1b_Ga=d,d=A,q=2\x1b\\" }

// DeleteAllKittyPlacements deletes placements while retaining uploaded data.
func DeleteAllKittyPlacements() string { return "\x1b_Ga=d,d=a,q=2\x1b\\" }

// KittyImageMetadata is the cell and pixel geometry associated with an image ID.
type KittyImageMetadata struct {
	ImageID  int
	Columns  int
	Rows     int
	WidthPx  int
	HeightPx int
}

// ImageProtocol identifies the terminal graphics protocol.
type ImageProtocol string

const (
	ImageNone   ImageProtocol = ""
	ImageKitty  ImageProtocol = "kitty"
	ImageITerm2 ImageProtocol = "iterm2"
)

// TerminalCapabilities are the capabilities used by renderer-independent core
// behavior. Environment detection is added with the concrete terminal driver.
type TerminalCapabilities struct {
	Images     ImageProtocol
	TrueColor  bool
	Hyperlinks bool
}

// CellDimensions are terminal cell dimensions in pixels.
type CellDimensions struct {
	WidthPx  int `json:"widthPx"`
	HeightPx int `json:"heightPx"`
}

var (
	terminalCapabilities = TerminalCapabilities{}
	cellDimensions       = CellDimensions{WidthPx: 9, HeightPx: 18}
)

// GetCapabilities returns the current terminal capability set.
func GetCapabilities() TerminalCapabilities { return terminalCapabilities }

// SetCapabilities overrides terminal capabilities, primarily for tests and
// explicit terminal-driver detection.
func SetCapabilities(capabilities TerminalCapabilities) { terminalCapabilities = capabilities }

// GetCellDimensions returns the most recent terminal cell-size reply.
func GetCellDimensions() CellDimensions { return cellDimensions }

// SetCellDimensions updates terminal cell dimensions.
func SetCellDimensions(dimensions CellDimensions) { cellDimensions = dimensions }

var (
	kittyImageMetadata               = map[int]registeredKittyImageMetadata{}
	kittyImageOrder                  []int
	kittyImageTransmissionGeneration uint64
)

type registeredKittyImageMetadata struct {
	KittyImageMetadata
	transmissionGeneration uint64
}

// KittyImagePlacement describes a registered Kitty transmission and its
// placement-only replacement command.
type KittyImagePlacement struct {
	ImageID                int
	TransmissionGeneration uint64
	TransmissionBytes      int
	EstimatedDecodedBytes  int64
	Sequence               string
	ReplacementLine        string
}

var kittyControlsPattern = regexp.MustCompile(`\x1b_G([^;]*);`)

// RegisterKittyImageMetadata records geometry needed to crop image placements.
func RegisterKittyImageMetadata(metadata KittyImageMetadata) {
	kittyImageTransmissionGeneration++
	if _, exists := kittyImageMetadata[metadata.ImageID]; exists {
		for index, imageID := range kittyImageOrder {
			if imageID == metadata.ImageID {
				kittyImageOrder = append(kittyImageOrder[:index], kittyImageOrder[index+1:]...)
				break
			}
		}
	}
	kittyImageMetadata[metadata.ImageID] = registeredKittyImageMetadata{
		KittyImageMetadata:     metadata,
		transmissionGeneration: kittyImageTransmissionGeneration,
	}
	kittyImageOrder = append(kittyImageOrder, metadata.ImageID)
	if len(kittyImageMetadata) <= 1000 {
		return
	}
	oldest := kittyImageOrder[0]
	kittyImageOrder = kittyImageOrder[1:]
	delete(kittyImageMetadata, oldest)
}

// GetKittyImageMetadata resolves a registered image ID from a Kitty command.
func GetKittyImageMetadata(line string) (KittyImageMetadata, bool) {
	metadata, ok := getRegisteredKittyImageMetadata(line)
	if !ok {
		return KittyImageMetadata{}, false
	}
	return metadata.KittyImageMetadata, true
}

func getRegisteredKittyImageMetadata(line string) (registeredKittyImageMetadata, bool) {
	match := kittyControlsPattern.FindStringSubmatch(line)
	if len(match) < 2 || match[1] == "" {
		return registeredKittyImageMetadata{}, false
	}
	for _, control := range strings.Split(match[1], ",") {
		if !strings.HasPrefix(control, "i=") {
			continue
		}
		value := strings.TrimPrefix(control, "i=")
		if value == "" {
			continue
		}
		valid := true
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		imageID, err := strconv.Atoi(value)
		if err != nil {
			return registeredKittyImageMetadata{}, false
		}
		metadata, ok := kittyImageMetadata[imageID]
		return metadata, ok
	}
	return registeredKittyImageMetadata{}, false
}

var kittyPlacementControlKeys = map[string]struct{}{
	"i": {}, "p": {}, "x": {}, "y": {}, "w": {}, "h": {}, "X": {}, "Y": {},
	"c": {}, "r": {}, "C": {}, "U": {}, "z": {}, "P": {}, "Q": {}, "H": {}, "V": {},
}

// GetKittyImagePlacement builds a placement-only command for a registered
// Kitty image transmission.
func GetKittyImagePlacement(line string) (KittyImagePlacement, bool) {
	location := kittyControlsPattern.FindStringSubmatchIndex(line)
	metadata, ok := getRegisteredKittyImageMetadata(line)
	if len(location) < 4 || !ok {
		return KittyImagePlacement{}, false
	}
	matchStart := location[0]
	commandStart := matchStart
	commandControls := line[location[2]:location[3]]
	transmissionEnd := 0
	for {
		terminatorRelative := strings.Index(line[commandStart+len(kittyImagePrefix):], "\x1b\\")
		if terminatorRelative == -1 {
			return KittyImagePlacement{}, false
		}
		transmissionEnd = commandStart + len(kittyImagePrefix) + terminatorRelative + 2
		if !hasKittyControl(commandControls, "m", "1") {
			break
		}
		commandStart = transmissionEnd
		if !strings.HasPrefix(line[commandStart:], kittyImagePrefix) {
			return KittyImagePlacement{}, false
		}
		controlsStart := commandStart + len(kittyImagePrefix)
		controlsEndRelative := strings.IndexByte(line[controlsStart:], ';')
		if controlsEndRelative == -1 {
			return KittyImagePlacement{}, false
		}
		commandControls = line[controlsStart : controlsStart+controlsEndRelative]
	}

	firstControls := line[location[2]:location[3]]
	retained := make([]string, 0)
	for _, control := range strings.Split(firstControls, ",") {
		key := control
		if separator := strings.IndexByte(key, '='); separator != -1 {
			key = key[:separator]
		}
		if _, keep := kittyPlacementControlKeys[key]; keep {
			retained = append(retained, control)
		}
	}
	sequence := kittyImagePrefix + "a=p,q=2," + strings.Join(retained, ",") + "\x1b\\"
	return KittyImagePlacement{
		ImageID:                metadata.ImageID,
		TransmissionGeneration: metadata.transmissionGeneration,
		TransmissionBytes:      transmissionEnd - matchStart,
		EstimatedDecodedBytes:  int64(metadata.WidthPx) * int64(metadata.HeightPx) * 4,
		Sequence:               sequence,
		ReplacementLine:        line[:matchStart] + sequence + line[transmissionEnd:],
	}, true
}

func hasKittyControl(controls, key, value string) bool {
	needle := key + "=" + value
	for _, control := range strings.Split(controls, ",") {
		if control == needle {
			return true
		}
	}
	return false
}

// CropKittyImageLine changes a placement to display a vertical source slice.
func CropKittyImageLine(line string, hiddenRows, visibleRows int) string {
	metadata, ok := GetKittyImageMetadata(line)
	location := kittyControlsPattern.FindStringSubmatchIndex(line)
	if !ok || len(location) < 4 || hiddenRows < 0 || hiddenRows >= metadata.Rows || visibleRows <= 0 {
		return line
	}
	croppedRows := min(visibleRows, metadata.Rows-hiddenRows)
	if hiddenRows == 0 && croppedRows == metadata.Rows {
		return line
	}
	sourceY := metadata.HeightPx * hiddenRows / metadata.Rows
	sourceEnd := int(math.Ceil(float64(metadata.HeightPx*(hiddenRows+croppedRows)) / float64(metadata.Rows)))
	sourceHeight := max(1, min(metadata.HeightPx, sourceEnd)-sourceY)
	controls := strings.Split(line[location[2]:location[3]], ",")
	filtered := make([]string, 0, len(controls)+3)
	for _, control := range controls {
		if strings.HasPrefix(control, "y=") || strings.HasPrefix(control, "h=") || strings.HasPrefix(control, "r=") {
			continue
		}
		filtered = append(filtered, control)
	}
	filtered = append(filtered,
		"y="+strconv.Itoa(sourceY),
		"h="+strconv.Itoa(sourceHeight),
		"r="+strconv.Itoa(croppedRows),
	)
	return line[:location[0]] + kittyImagePrefix + strings.Join(filtered, ",") + ";" + line[location[1]:]
}

//go:build !((linux || windows) && (amd64 || arm64))

package gpu

func openNVDEC(DecodeConfig, func(*DecodedPicture) error) (Decoder, error) {
	return nil, ErrDecodeUnavailable
}

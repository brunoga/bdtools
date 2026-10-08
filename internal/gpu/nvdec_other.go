//go:build !(linux && (amd64 || arm64))

package gpu

func openNVDEC(DecodeConfig, func(*DecodedPicture) error) (Decoder, error) {
	return nil, ErrDecodeUnavailable
}

//go:build !(linux && (amd64 || arm64))

package gpu

import "io"

func openVAAPI(Config, io.Writer) (Encoder, error) { return nil, ErrUnavailable }

func openVAAPIDecoder(DecodeConfig, func(*DecodedPicture) error) (Decoder, error) {
	return nil, ErrDecodeUnavailable
}

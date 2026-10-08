//go:build !(darwin && (amd64 || arm64))

package gpu

import "io"

func openVideoToolbox(Config, io.Writer) (Encoder, error) { return nil, ErrUnavailable }

func openVTDecoder(DecodeConfig, func(*DecodedPicture) error) (Decoder, error) {
	return nil, ErrDecodeUnavailable
}

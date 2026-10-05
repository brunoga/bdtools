//go:build !((linux || windows) && (amd64 || arm64))

package hwenc

import "io"

func openNVENC(Config, io.Writer) (Encoder, error) { return nil, ErrUnavailable }

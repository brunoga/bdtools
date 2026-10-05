//go:build !(linux && (amd64 || arm64))

package hwenc

import "io"

func openVAAPI(Config, io.Writer) (Encoder, error) { return nil, ErrUnavailable }

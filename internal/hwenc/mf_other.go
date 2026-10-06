//go:build !(windows && (amd64 || arm64))

package hwenc

import "io"

func openMediaFoundation(Config, io.Writer) (Encoder, error) { return nil, ErrUnavailable }

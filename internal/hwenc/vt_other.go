//go:build !(darwin && (amd64 || arm64))

package hwenc

import "io"

func openVideoToolbox(Config, io.Writer) (Encoder, error) { return nil, ErrUnavailable }

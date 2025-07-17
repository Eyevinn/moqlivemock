package main

import (
	"encoding/base64"
	"io"
)

func unpackWrite(initData string, w io.Writer) error {
	initBytes, err := base64.StdEncoding.DecodeString(initData)
	if err != nil {
		return err
	}
	_, err = w.Write(initBytes)
	if err != nil {
		return err
	}
	return nil
}

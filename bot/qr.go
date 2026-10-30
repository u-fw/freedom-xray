package main

import (
	"fmt"

	"github.com/skip2/go-qrcode"
)

// GenerateQRCodePNG encodes content into a PNG byte slice
func GenerateQRCodePNG(content string, size int) ([]byte, error) {
	if size <= 0 {
		size = 256
	}
	png, err := qrcode.Encode(content, qrcode.Medium, size)
	if err != nil {
		return nil, fmt.Errorf("failed to generate qr code: %w", err)
	}
	return png, nil
}

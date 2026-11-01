package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

const botReadyMarker = "/run/freedom-bot.ready"

func executableSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sum := sha256.New()
	if _, err = io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// Only publish readiness after the real Bot API has accepted the token.
// The external update watchdog verifies that the running process belongs
// to the downloaded executable, not a stale pre-update process.
func publishBotReadiness() error {
	path, err := os.Executable()
	if err != nil {
		return err
	}
	digest, err := executableSHA256(path)
	if err != nil {
		return err
	}
	if err = os.WriteFile(botReadyMarker, []byte(digest+"\n"), 0600); err != nil {
		return fmt.Errorf("cannot record Bot readiness: %w", err)
	}
	return nil
}

package main

import (
	"encoding/json"
	"errors"
	"os"
	"time"
)

func persistServerHost(path, value string) (string, error) {
	if path == "" {
		return "", errors.New("Bot configuration path is unavailable")
	}
	host, err := normalizeServerHost(value)
	if err != nil {
		return "", err
	}
	lock, err := acquireConfigLock(path, 10*time.Second)
	if err != nil {
		return "", err
	}
	defer releaseConfigLock(lock)
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	if doc == nil {
		return "", errors.New("Bot configuration must be an object")
	}
	if host == "" {
		delete(doc, "server_host")
	} else {
		doc["server_host"], _ = json.Marshal(host)
	}
	updated, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	if err = writeAtomically(path, append(updated, '\n'), 0600); err != nil {
		return "", err
	}
	return host, nil
}

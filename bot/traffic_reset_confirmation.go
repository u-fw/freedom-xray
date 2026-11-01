package main

import (
	"errors"
	"sync"
	"time"
)

var errTrafficResetIdentityChanged = errors.New("user identity changed; review the reset again")

type pendingTrafficReset struct {
	ChatID   int64
	AdminID  int64
	User     string
	Identity string
	Expires  time.Time
}

var pendingTrafficResets = struct {
	sync.Mutex
	items map[string]pendingTrafficReset
}{items: map[string]pendingTrafficReset{}}

func managedUserIdentityFingerprintLocked(configPath, user string) (string, error) {
	root, err := decodeBindingMigrationConfig(configPath)
	if err != nil {
		return "", err
	}
	directory, _, err := loadUserDirectory(configPath, root)
	if err != nil {
		return "", err
	}
	identity, ok := directory.Users[user]
	if !ok || identity.ID == "" {
		return "", errTrafficResetIdentityChanged
	}
	return routingRuleETag(map[string]any{"user": user, "id": identity.ID})
}

func managedUserIdentityFingerprint(configPath, user string) (string, error) {
	lock, err := acquireConfigLock(configPath, 10*time.Second)
	if err != nil {
		return "", err
	}
	defer releaseConfigLock(lock)
	return managedUserIdentityFingerprintLocked(configPath, user)
}

func resetUserTrafficWithIdentity(configPath, xrayBinary, statsServer, user, identity string) error {
	lock, err := acquireConfigLock(configPath, 10*time.Second)
	if err != nil {
		return err
	}
	defer releaseConfigLock(lock)
	current, err := managedUserIdentityFingerprintLocked(configPath, user)
	if err != nil {
		return err
	}
	if identity == "" || current != identity {
		return errTrafficResetIdentityChanged
	}
	return resetUserTrafficLocked(configPath, xrayBinary, statsServer, user)
}

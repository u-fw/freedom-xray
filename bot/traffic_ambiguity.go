package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

const nativeTrafficAmbiguityPrefix = "native_traffic_ambiguous:"

// Xray stats are keyed by client email, not by protocol. A native account
// using a managed VLESS identity would otherwise contribute to that user's
// quota and a reset could clear the native account's runtime counters.
func nativeAccountOwners(root map[string]any) (map[string]bool, error) {
	values, err := nativeInboundValues(root)
	if err != nil {
		return nil, err
	}
	owners := map[string]bool{}
	var visit func(any)
	visit = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			for key, child := range item {
				if strings.EqualFold(key, "email") {
					if email, ok := child.(string); ok && email != "" {
						owner := bindingOwner(email)
						if managedUserName.MatchString(owner) {
							owners[owner] = true
						}
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range item {
				visit(child)
			}
		}
	}
	for _, value := range values {
		inbound, ok := getMap(value)
		if !ok {
			return nil, errors.New("invalid inbound object")
		}
		protocol, _ := inbound["protocol"].(string)
		if !strings.EqualFold(protocol, "vless") {
			visit(inbound["settings"])
		}
	}
	return owners, nil
}

func validateNativeInboundIdentities(root, inbound map[string]any, configPath string) error {
	owners, err := nativeAccountOwners(map[string]any{"inbounds": []any{inbound}})
	if err != nil || len(owners) == 0 {
		return err
	}
	var directory userDirectory
	if configPath == "" {
		directory, err = directoryFromConfig(root)
	} else {
		directory, _, err = loadUserDirectory(configPath, root)
	}
	if err != nil {
		return err
	}
	for owner := range owners {
		if _, exists := directory.Users[owner]; exists {
			return errors.New("a native account email conflicts with a managed VLESS user; choose a distinct email")
		}
	}
	if configPath != "" {
		suspended, _, err := readSuspendedStore(configPath)
		if err != nil {
			return err
		}
		for owner := range owners {
			if _, exists := suspended.Users[owner]; exists {
				return errors.New("a native account email conflicts with a suspended VLESS user; choose a distinct email")
			}
		}
	}
	return nil
}

func storedTrafficAmbiguities(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query("SELECT key FROM meta WHERE key GLOB 'native_traffic_ambiguous:*'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(key, nativeTrafficAmbiguityPrefix)
		if managedUserName.MatchString(name) {
			result[name] = true
		}
	}
	return result, rows.Err()
}

// Record ambiguity before sampling or replacing configuration. Once counters
// may be mixed, only an explicit safe per-user reset clears the marker.
func ensureTrafficAmbiguitiesLocked(configPath string) error {
	root, err := decodeBindingMigrationConfig(configPath)
	if err != nil {
		return err
	}
	owners, err := nativeAccountOwners(root)
	if err != nil || len(owners) == 0 {
		return err
	}
	directory, _, err := loadUserDirectory(configPath, root)
	if err != nil {
		return err
	}
	suspended, _, err := readSuspendedStore(configPath)
	if err != nil {
		return err
	}
	db, err := openTrafficDB(configPath)
	if err != nil {
		return err
	}
	defer db.Close()
	marked, err := storedTrafficAmbiguities(db)
	if err != nil {
		return err
	}
	for owner := range owners {
		_, managed := directory.Users[owner]
		_, parked := suspended.Users[owner]
		if (managed || parked) && !marked[owner] {
			if _, err := db.Exec("INSERT OR IGNORE INTO meta(key,value) VALUES(?,?)", nativeTrafficAmbiguityPrefix+owner, "1"); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureTrafficAmbiguities(configPath string) error {
	lock, err := acquireConfigLock(configPath, 20*time.Second)
	if err != nil {
		return err
	}
	defer releaseConfigLock(lock)
	return ensureTrafficAmbiguitiesLocked(configPath)
}

func trafficAmbiguities(configPath string, root map[string]any) (map[string]bool, error) {
	owners, err := nativeAccountOwners(root)
	if err != nil {
		return nil, err
	}
	db, err := openTrafficDB(configPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	marked, err := storedTrafficAmbiguities(db)
	if err != nil {
		return nil, err
	}
	for owner := range owners {
		marked[owner] = true
	}
	return marked, nil
}

func clearTrafficAmbiguityLocked(configPath, user string) error {
	if !managedUserName.MatchString(user) {
		return errors.New("invalid user identifier")
	}
	return setTrafficMetaValue(configPath, nativeTrafficAmbiguityPrefix+user, "")
}

func moveTrafficAmbiguityLocked(configPath, oldName, newName string) error {
	value, err := trafficMetaValue(configPath, nativeTrafficAmbiguityPrefix+oldName)
	if err != nil || value == "" {
		return err
	}
	if newName != "" {
		if err := setTrafficMetaValue(configPath, nativeTrafficAmbiguityPrefix+newName, value); err != nil {
			return err
		}
	}
	return clearTrafficAmbiguityLocked(configPath, oldName)
}

func copyTrafficAmbiguityLocked(configPath, oldName, newName string) error {
	value, err := trafficMetaValue(configPath, nativeTrafficAmbiguityPrefix+oldName)
	if err != nil || value == "" {
		return err
	}
	return setTrafficMetaValue(configPath, nativeTrafficAmbiguityPrefix+newName, value)
}
